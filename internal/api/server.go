package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"proxyclient/internal/config"
	"proxyclient/internal/eventlog"
	"proxyclient/internal/logger"
	"proxyclient/internal/proxy"
	"proxyclient/internal/wintun"
	"proxyclient/internal/xray"

	"github.com/gorilla/mux"
)

// DefaultProxyAddress адрес HTTP-прокси по умолчанию (sing-box http inbound)
// DefaultProxyAddress адрес HTTP-прокси по умолчанию — алиас config.ProxyAddr.
const DefaultProxyAddress = config.ProxyAddr

// Config конфигурация API сервера
type Config struct {
	ListenAddress string
	XRayManager   xray.Manager
	ProxyManager  proxy.Manager
	ConfigPath    string
	SecretKeyPath string // путь до secret.key (active VLESS URL)
	Logger        logger.Logger
	EventLog      *eventlog.Log // может быть nil — тогда /api/events недоступен
	// QuitChan если задан — закрывается при вызове POST /api/quit,
	// что запускает graceful shutdown всего приложения (аналог кнопки "Выход" в трее).
	QuitChan chan struct{}
	// SilentPaths — пути которые не нужно логировать (polling endpoints)
	SilentPaths []string
}

// Server HTTP API сервер
type Server struct {
	config          Config
	configMu        sync.RWMutex // защищает изменяемые поля config (XRayManager)
	proxyOpMu       sync.Mutex   // сериализует check+act операции над прокси (устраняет TOCTOU)
	router          *mux.Router
	httpServer      *http.Server
	logger          logger.Logger
	quitOnce        sync.Once    // гарантирует однократное закрытие QuitChan (защита от двойного POST /api/quit)
	lifecycleCtx    context.Context // отменяется при Shutdown — прерывает PollUntilFree в tun_handlers
	// BUG FIX #13: restarting и restartReadyAt объединены под одним мьютексом.
	// /api/status поллится каждые 2-3с — два отдельных RLock были избыточны.
	restartMu        sync.RWMutex
	restarting       bool
	restartReadyAt   time.Time
	tunAttempt       int // текущая попытка поднять TUN
	tunMaxAttempt    int // максимум попыток
	// BUG FIX #7: silentCache строится один раз, инвалидируется только при addSilentPath.
	// Без кэша map пересоздавалась при каждом HTTP-запросе (~30 аллокаций/мин).
	silentMu         sync.RWMutex
	silentCache      map[string]bool
}

// StatusResponse ответ для /api/status
type StatusResponse struct {
	XRay struct {
		Running     bool  `json:"running"`
		PID         int   `json:"pid"`
		Warming     bool  `json:"warming"`     // true пока wintun/sing-box ещё инициализируются
		ReadyAt     int64 `json:"ready_at"`    // Unix timestamp готовности (0 если не в прогреве)
		TunAttempt  int   `json:"tun_attempt"` // текущая попытка поднять TUN (0 = не в режиме повторов)
		TunMaxAttempt int `json:"tun_max_attempt"` // максимум попыток
	} `json:"xray"`
	Proxy struct {
		Enabled bool   `json:"enabled"`
		Address string `json:"address"`
	} `json:"proxy"`
	ConfigPath string `json:"config_path"`
}

// ErrorResponse ответ с ошибкой
type ErrorResponse struct {
	Error string `json:"error"`
}

// MessageResponse простой ответ с сообщением
type MessageResponse struct {
	Message string `json:"message"`
	Success bool   `json:"success"`
}

// NewServer создаёт новый API сервер.
// XRayManager в cfg может быть nil — это нормально при "фоновом старте":
// UI поднимается сразу, sing-box стартует позже и обновляет менеджер через SetXRayManager.
func NewServer(cfg Config, lifecycleCtx context.Context) *Server {
	if lifecycleCtx == nil {
		lifecycleCtx = context.Background()
	}
	s := &Server{
		config:       cfg,
		logger:       cfg.Logger,
		router:       mux.NewRouter(),
		lifecycleCtx: lifecycleCtx,
	}
	s.setupRoutes()
	return s
}

// setupRoutes регистрирует базовые API маршруты (без статики)
func (s *Server) setupRoutes() {
	s.router.Use(s.corsMiddleware)
	s.router.Use(s.loggingMiddleware)
	s.router.Use(s.recoveryMiddleware)

	api := s.router.PathPrefix("/api").Subrouter()
	api.HandleFunc("/status", s.handleStatus).Methods("GET", "OPTIONS")
	api.HandleFunc("/proxy/enable", s.handleProxyEnable).Methods("POST", "OPTIONS")
	api.HandleFunc("/proxy/disable", s.handleProxyDisable).Methods("POST", "OPTIONS")
	api.HandleFunc("/proxy/toggle", s.handleProxyToggle).Methods("POST", "OPTIONS")
	api.HandleFunc("/health", s.handleHealth).Methods("GET", "OPTIONS")
	api.HandleFunc("/quit", s.handleQuit).Methods("POST", "OPTIONS")
	api.HandleFunc("/events", s.handleEvents).Methods("GET", "OPTIONS")
	api.HandleFunc("/events/clear", s.handleEventsClear).Methods("POST", "OPTIONS")
}

// SetupFeatureRoutes регистрирует профили, диагностику и статистику.
// ctx используется для управления lifecycle фоновых горутин диагностики —
// при отмене ctx горутины останавливаются корректно.
// Вызывать после NewServer, до FinalizeRoutes.
func (s *Server) SetupFeatureRoutes(ctx context.Context) {
	SetupProfileRoutes(s)
	SetupDiagRoutes(s, ctx)
	SetupSettingsRoutes(s)
	SetupEngineRoutes(s)
	if s.config.SecretKeyPath != "" {
		SetupServerRoutes(s, s.config.SecretKeyPath)
	}
	// Geosite endpoints
	api := s.router.PathPrefix("/api").Subrouter()
	api.HandleFunc("/geosite", s.handleGeositeList).Methods("GET")
	api.HandleFunc("/geosite/download", s.handleGeositeDownload).Methods("POST")
	// Polling-эндпоинты которые не нужно логировать
	s.addSilentPath("/api/stats")
	s.addSilentPath("/api/connections")
}

// FinalizeRoutes регистрирует статику — вызывать после всех других маршрутов
func (s *Server) FinalizeRoutes() {
	s.router.PathPrefix("/").Handler(staticHandler())
}

// Start запускает HTTP сервер
func (s *Server) Start(ctx context.Context) error {
	s.httpServer = &http.Server{
		Addr:    s.config.ListenAddress,
		Handler: s.router,
		// ReadHeaderTimeout вместо ReadTimeout: читаем только заголовки с таймаутом.
		// ReadTimeout обрывал бы streaming эндпоинты (/traffic от sing-box).
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      15 * time.Second,
		// IdleTimeout 120с: UI поллит каждые 2-3с — keep-alive устраняет TCP
		// handshake для каждого polling запроса (~1мс экономия × 30 req/min).
		IdleTimeout: 120 * time.Second,
	}

	errChan := make(chan error, 1)
	go func() {
		s.logger.Info("API сервер запущен на %s", s.config.ListenAddress)
		if err := s.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errChan <- err
		}
	}()

	select {
	case err := <-errChan:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.httpServer.Shutdown(shutdownCtx)
	}
}

// Shutdown корректно останавливает сервер
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpServer == nil {
		return nil
	}
	s.logger.Info("Остановка API сервера...")
	err := s.httpServer.Shutdown(ctx)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("ошибка при остановке API сервера: %w", err)
	}
	s.logger.Info("API сервер остановлен")
	return nil
}

// GetXRayManager возвращает актуальный XRayManager (обновляется после doApply и после фонового старта).
func (s *Server) GetXRayManager() xray.Manager {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.config.XRayManager
}

// SetXRayManager обновляет XRayManager потокобезопасно.
// Используется при "фоновом старте": sing-box создаётся после поднятия UI
// и регистрирует себя здесь когда готов.
// Также используется в doApply при перезапуске с новым конфигом.
func (s *Server) SetXRayManager(mgr xray.Manager) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	s.config.XRayManager = mgr
}

// IsWarming возвращает true пока XRayManager ещё не установлен (фоновая инициализация).
func (s *Server) IsWarming() bool {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.config.XRayManager == nil
}

// SetRestarting помечает что идёт перезапуск после краша и задаёт ETA готовности.
// Вызывается из handleCrash перед PollUntilFree.
func (s *Server) SetRestarting(readyAt time.Time) {
	s.restartMu.Lock()
	defer s.restartMu.Unlock()
	s.restarting = true
	s.restartReadyAt = readyAt
}

// SetTunAttempt обновляет счётчик попыток поднять TUN — отображается в UI.
func (s *Server) SetTunAttempt(attempt, maxAttempt int) {
	s.restartMu.Lock()
	defer s.restartMu.Unlock()
	s.tunAttempt = attempt
	s.tunMaxAttempt = maxAttempt
}

// ClearRestarting сбрасывает флаг перезапуска — вызывается после успешного старта.
func (s *Server) ClearRestarting() {
	s.restartMu.Lock()
	defer s.restartMu.Unlock()
	s.restarting = false
	s.restartReadyAt = time.Time{}
	s.tunAttempt = 0
	s.tunMaxAttempt = 0
}

// handleStatus GET /api/status
func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	proxyConfig := s.config.ProxyManager.GetConfig()

	s.configMu.RLock()
	xrayMgr := s.config.XRayManager
	s.configMu.RUnlock()

	response := StatusResponse{
		ConfigPath: s.config.ConfigPath,
	}

	s.restartMu.RLock()
	restarting := s.restarting
	restartReadyAt := s.restartReadyAt
	s.restartMu.RUnlock()

	if xrayMgr == nil {
		// Фоновая инициализация ещё не завершена — сообщаем UI о прогреве.
		response.XRay.Running = false
		response.XRay.Warming = true
		if eta := wintun.EstimateReadyAt(); eta.After(time.Now()) {
			response.XRay.ReadyAt = eta.Unix()
		}
	} else if restarting {
		// Sing-box упал, идёт wintun cleanup — показываем countdown.
		response.XRay.Running = false
		response.XRay.Warming = true
		if restartReadyAt.After(time.Now()) {
			response.XRay.ReadyAt = restartReadyAt.Unix()
		}
		response.XRay.TunAttempt = s.tunAttempt
		response.XRay.TunMaxAttempt = s.tunMaxAttempt
	} else {
		response.XRay.Running = xrayMgr.IsRunning()
		response.XRay.PID = xrayMgr.GetPID()
		response.XRay.Warming = false
		response.XRay.ReadyAt = 0
	}
	response.Proxy.Enabled = s.config.ProxyManager.IsEnabled()
	response.Proxy.Address = proxyConfig.Address

	s.respondJSON(w, http.StatusOK, response)
}

// handleProxyEnable POST /api/proxy/enable
func (s *Server) handleProxyEnable(w http.ResponseWriter, _ *http.Request) {
	s.proxyOpMu.Lock()
	defer s.proxyOpMu.Unlock()

	if s.config.ProxyManager.IsEnabled() {
		s.respondError(w, http.StatusBadRequest, "прокси уже включен")
		return
	}
	if err := s.config.ProxyManager.Enable(proxy.Config{
		Address:  DefaultProxyAddress,
		Override: "<local>",
	}); err != nil {
		s.logger.Error("Не удалось включить прокси: %v", err)
		s.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.respondJSON(w, http.StatusOK, MessageResponse{Message: "Прокси успешно включен", Success: true})
}

// handleProxyDisable POST /api/proxy/disable
func (s *Server) handleProxyDisable(w http.ResponseWriter, _ *http.Request) {
	s.proxyOpMu.Lock()
	defer s.proxyOpMu.Unlock()

	if !s.config.ProxyManager.IsEnabled() {
		s.respondError(w, http.StatusBadRequest, "прокси уже отключен")
		return
	}
	if err := s.config.ProxyManager.Disable(); err != nil {
		s.logger.Error("Не удалось отключить прокси: %v", err)
		s.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.respondJSON(w, http.StatusOK, MessageResponse{Message: "Прокси успешно отключен", Success: true})
}

// handleProxyToggle POST /api/proxy/toggle
func (s *Server) handleProxyToggle(w http.ResponseWriter, _ *http.Request) {
	s.proxyOpMu.Lock()
	defer s.proxyOpMu.Unlock()

	if s.config.ProxyManager.IsEnabled() {
		if err := s.config.ProxyManager.Disable(); err != nil {
			s.logger.Error("Не удалось отключить прокси: %v", err)
			s.respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.respondJSON(w, http.StatusOK, MessageResponse{Message: "Прокси отключен", Success: true})
		return
	}
	if err := s.config.ProxyManager.Enable(proxy.Config{
		Address:  DefaultProxyAddress,
		Override: "<local>",
	}); err != nil {
		s.logger.Error("Не удалось включить прокси: %v", err)
		s.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.respondJSON(w, http.StatusOK, MessageResponse{Message: "Прокси включен", Success: true})
}

// handleHealth GET /api/health
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	s.respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleQuit POST /api/quit — инициирует graceful shutdown всего приложения
func (s *Server) handleQuit(w http.ResponseWriter, _ *http.Request) {
	s.respondJSON(w, http.StatusOK, MessageResponse{Message: "shutting down", Success: true})
	if s.config.QuitChan != nil {
		go func() {
			time.Sleep(100 * time.Millisecond)
			s.quitOnce.Do(func() { close(s.config.QuitChan) })
		}()
	}
}

// respondJSON отправляет JSON ответ
func (s *Server) respondJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		s.logger.Error("Ошибка при кодировании JSON: %v", err)
	}
}

// respondError отправляет ошибку в JSON формате
func (s *Server) respondError(w http.ResponseWriter, status int, message string) {
	s.respondJSON(w, status, ErrorResponse{Error: message})
}

// corsMiddleware добавляет CORS заголовки.
// BUG FIX #19: ранее Access-Control-Allow-Origin: * разрешал любому сайту
// в браузере делать запросы к API (включая POST /api/quit).
// Теперь разрешены только localhost origins и app:// для WebView2.
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	allowed := map[string]bool{
		"http://localhost:8080":  true,
		"http://127.0.0.1:8080": true,
		"app://":                 true, // WebView2 custom scheme
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" || allowed[origin] {
			// Запросы без Origin (curl, Postman, нативные) или с разрешённого origin.
			if origin != "" {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
			}
		} else {
			// Запрос с чужого origin — отклоняем preflight, для остальных не ставим заголовок.
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			// Остальные запросы пропускаем без CORS заголовков — браузер заблокирует сам.
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// addSilentPath добавляет путь в список без логирования (polling endpoints)
func (s *Server) addSilentPath(path string) {
	s.config.SilentPaths = append(s.config.SilentPaths, path)
	// Инвалидируем кэш silentPaths в loggingMiddleware — он будет пересобран при следующем запросе.
	s.silentMu.Lock()
	s.silentCache = nil
	s.silentMu.Unlock()
}

// loggingMiddleware логирует HTTP запросы.
// BUG FIX #7: silentPaths map пересоздавалась на каждый HTTP-запрос (~30 аллокаций/мин).
// Теперь map строится один раз и инвалидируется только при вызове addSilentPath.
func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.silentMu.RLock()
		cache := s.silentCache
		s.silentMu.RUnlock()

		if cache == nil {
			// Собираем map — редкое событие (только при старте или addSilentPath).
			s.silentMu.Lock()
			if s.silentCache == nil { // double-checked locking
				m := map[string]bool{
					"/api/status":           true,
					"/api/health":           true,
					"/api/tun/apply/status": true,
					"/api/events":           true,
				}
				for _, p := range s.config.SilentPaths {
					m[p] = true
				}
				s.silentCache = m
			}
			cache = s.silentCache
			s.silentMu.Unlock()
		}

		if cache[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, ".js") ||
			strings.HasSuffix(r.URL.Path, ".css") ||
			strings.HasSuffix(r.URL.Path, ".ico") ||
			strings.HasSuffix(r.URL.Path, ".html") {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rw, r)
		s.logger.Info("%s %s - %d (%v)", r.Method, r.URL.Path, rw.statusCode, time.Since(start))
	})
}

// recoveryMiddleware обрабатывает панику
func (s *Server) recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				s.logger.Error("Паника в обработчике: %v", err)
				s.respondError(w, http.StatusInternalServerError, "внутренняя ошибка сервера")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// handleEvents GET /api/events?since=N — события с ID > since
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if s.config.EventLog == nil {
		s.respondJSON(w, http.StatusOK, map[string]interface{}{"events": []interface{}{}, "latest_id": 0})
		return
	}
	since := 0
	if v := r.URL.Query().Get("since"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			since = n
		}
	}
	events := s.config.EventLog.GetSince(since)
	if events == nil {
		events = []eventlog.Event{}
	}
	s.respondJSON(w, http.StatusOK, map[string]interface{}{
		"events":    events,
		"latest_id": s.config.EventLog.GetLatestID(),
	})
}

// handleEventsClear POST /api/events/clear — очищает буфер событий
func (s *Server) handleEventsClear(w http.ResponseWriter, _ *http.Request) {
	if s.config.EventLog != nil {
		s.config.EventLog.Clear()
	}
	s.respondJSON(w, http.StatusOK, MessageResponse{Message: "cleared", Success: true})
}

// responseWriter обёртка для захвата статус кода
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}
