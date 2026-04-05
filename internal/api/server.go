package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
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

// clashAPIURL базовый URL Clash API (Clash-совместимый API sing-box).
// Переопределяется в тестах чтобы тесты не зависели от реального Clash API.
var clashAPIURL = config.ClashAPIBase

// tokenBucket реализует алгоритм «ведро с токенами» для rate limiting.
// Потокобезопасен через mutex.
type tokenBucket struct {
	mu         sync.Mutex
	tokens     float64
	maxTokens  float64
	refillRate float64 // токенов в секунду
	lastRefill time.Time
}

func newTokenBucket(ratePerSec float64) *tokenBucket {
	return &tokenBucket{
		tokens:     ratePerSec,
		maxTokens:  ratePerSec,
		refillRate: ratePerSec,
		lastRefill: time.Now(),
	}
}

// allow проверяет и потребляет один токен.
// Возвращает true если запрос разрешён, false если лимит превышен.
func (tb *tokenBucket) allow() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	now := time.Now()
	elapsed := now.Sub(tb.lastRefill).Seconds()
	tb.tokens += elapsed * tb.refillRate
	if tb.tokens > tb.maxTokens {
		tb.tokens = tb.maxTokens
	}
	tb.lastRefill = now
	if tb.tokens >= 1.0 {
		tb.tokens--
		return true
	}
	return false
}

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
	QuitChan      chan struct{} // закрывается при вызове POST /api/quit
	SilentPaths   []string      // дополнительные пути, которые не нужно логировать

	// ManualTURNFn вызывается при POST /api/tun/turn для ручного переключения TURN туннеля.
	// Устанавливается через SetManualTURNFn после запуска turnmanager.
	// Может быть nil — тогда хендлер возвращает 503 (мониторинг ещё не стартовал).
	ManualTURNFn       func(bool) error
	SecretKeyUpdatedFn func()
}

// Server HTTP API сервер
type Server struct {
	config       Config
	configMu     sync.RWMutex
	proxyOpMu    sync.Mutex
	router       *mux.Router
	httpServer   *http.Server
	logger       logger.Logger
	quitOnce     sync.Once
	lifecycleCtx context.Context
	// rateLimiter — ограничитель частоты мутирующих запросов (POST/PUT/DELETE/PATCH).
	rateLimiter *tokenBucket

	restartMu      sync.RWMutex
	restarting     bool
	restartReadyAt time.Time
	tunAttempt     int
	tunMaxAttempt  int

	silentMu    sync.RWMutex
	silentCache map[string]bool
	tunHandlers *TunHandlers

	// turnMu защищает turnActive и turnManual.
	// turnActive=true означает что трафик идёт через TURN туннель (DTLS masquerade).
	// turnManual=true означает что TURN включён вручную пользователем (retryLoop не возвращает на direct).
	turnMu     sync.RWMutex
	turnActive bool
	turnManual bool

	proxyEnabledAtMu sync.RWMutex
	proxyEnabledAt   time.Time

	// B-2: Proxy Guard состояние
	proxyGuardMu      sync.RWMutex
	proxyGuardEnabled bool

	// B-10: автообновление geosite баз данных
	geoUpdater *GeoAutoUpdater
}

// StatusResponse ответ для /api/status
type StatusResponse struct {
	XRay struct {
		Running       bool    `json:"running"`
		PID           int     `json:"pid"`
		Warming       bool    `json:"warming"`
		ReadyAt       int64   `json:"ready_at"`
		TunAttempt    int     `json:"tun_attempt"`
		TunMaxAttempt int     `json:"tun_max_attempt"`
		HealthStatus  string  `json:"health_status"`  // "healthy", "degraded", "unavailable"
		ErrorCount    int     `json:"error_count"`    // Ошибок в окне мониторинга
		ErrorRatePct  float64 `json:"error_rate_pct"` // % ошибок из всех попыток
	} `json:"xray"`
	Proxy struct {
		Enabled bool   `json:"enabled"`
		Address string `json:"address"`
		UptimeS int64  `json:"proxy_uptime_secs"`
	} `json:"proxy"`
	ConfigPath string `json:"config_path"`
	// TurnActive=true когда трафик идёт через TURN туннель (маскировка под VK DTLS).
	TurnActive bool `json:"turn_active"`
	// TurnManual=true когда TURN включён вручную пользователем через UI.
	// В этом режиме retryLoop не возвращает на direct автоматически.
	TurnManual bool `json:"turn_manual"`
}

// SetTURNMode обновляет флаг активности TURN туннеля.
// Вызывается из app.go при переключении режима.
// Потокобезопасен.
func (s *Server) SetTURNMode(active bool) {
	s.turnMu.Lock()
	s.turnActive = active
	s.turnMu.Unlock()
}

// SetTURNManual обновляет флаг ручного управления TURN.
// Вызывается из ManualTURNFn когда пользователь явно включает/выключает TURN через UI.
// Потокобезопасен.
func (s *Server) SetTURNManual(enabled bool) {
	s.turnMu.Lock()
	s.turnManual = enabled
	s.turnMu.Unlock()
}

// SetManualTURNFn регистрирует функцию ручного переключения TURN туннеля.
// Вызывается из app.go после запуска turnmanager.
// Потокобезопасен.
func (s *Server) SetManualTURNFn(fn func(bool) error) {
	s.configMu.Lock()
	s.config.ManualTURNFn = fn
	s.configMu.Unlock()
}

// B-2: Proxy Guard getters/setters
func (s *Server) IsProxyGuardEnabled() bool {
	s.proxyGuardMu.RLock()
	defer s.proxyGuardMu.RUnlock()
	return s.proxyGuardEnabled
}

func (s *Server) SetProxyGuardEnabled(enabled bool) {
	s.proxyGuardMu.Lock()
	s.proxyGuardEnabled = enabled
	s.proxyGuardMu.Unlock()
}

// B-10: SetGeoAutoUpdater регистрирует updater для управления через API настроек.
func (s *Server) SetGeoAutoUpdater(g *GeoAutoUpdater) {
	s.geoUpdater = g
}

// B-10: GetGeoAutoUpdater возвращает текущий updater (может быть nil).
func (s *Server) GetGeoAutoUpdater() *GeoAutoUpdater {
	return s.geoUpdater
}

type ErrorResponse struct {
	Error string `json:"error"`
}

type MessageResponse struct {
	Message string `json:"message"`
	Success bool   `json:"success"`
}

// NewServer создаёт новый API сервер.
func NewServer(cfg Config, lifecycleCtx context.Context) *Server {
	if lifecycleCtx == nil {
		lifecycleCtx = context.Background()
	}
	s := &Server{
		config:       cfg,
		logger:       cfg.Logger,
		router:       mux.NewRouter(),
		lifecycleCtx: lifecycleCtx,
		rateLimiter:  newTokenBucket(5), // 5 мутирующих запросов в секунду
	}
	s.setupRoutes()
	return s
}

func (s *Server) setupRoutes() {
	s.router.Use(s.corsMiddleware)
	s.router.Use(s.loggingMiddleware)
	s.router.Use(s.recoveryMiddleware)
	s.router.Use(s.rateLimitMiddleware)
	s.router.Use(s.maxBodyMiddleware)

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

func (s *Server) SetupFeatureRoutes(ctx context.Context) {
	SetupProfileRoutes(s)
	SetupDiagRoutes(s, ctx)
	SetupSettingsRoutes(s)
	SetupEngineRoutes(s)
	if s.config.SecretKeyPath != "" {
		SetupServerRoutes(s, s.config.SecretKeyPath)
	}

	api := s.router.PathPrefix("/api").Subrouter()
	api.HandleFunc("/geosite", s.handleGeositeList).Methods("GET")
	api.HandleFunc("/geosite/download", s.handleGeositeDownload).Methods("POST")
	// B-8: Backup и restore endpoints
	api.HandleFunc("/backup", handleBackup).Methods("GET", "OPTIONS")
	api.HandleFunc("/backup/restore", handleBackupRestore).Methods("POST", "OPTIONS")

	s.addSilentPath("/api/stats")
	s.addSilentPath("/api/connections")
}

func (s *Server) FinalizeRoutes() {
	s.router.PathPrefix("/").Handler(staticHandler())
}

func (s *Server) Start(ctx context.Context) error {
	s.httpServer = &http.Server{
		Addr:              s.config.ListenAddress,
		Handler:           s.router,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       120 * time.Second,
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

func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpServer == nil {
		return nil
	}
	s.logger.Info("Остановка API сервера...")
	err := s.httpServer.Shutdown(ctx)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("ошибка при остановке API сервера: %w", err)
	}
	return nil
}

func (s *Server) GetXRayManager() xray.Manager {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.config.XRayManager
}

func (s *Server) SetXRayManager(mgr xray.Manager) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	s.config.XRayManager = mgr
}

func (s *Server) IsWarming() bool {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.config.XRayManager == nil
}

func (s *Server) SetRestarting(readyAt time.Time) {
	s.restartMu.Lock()
	defer s.restartMu.Unlock()
	s.restarting = true
	s.restartReadyAt = readyAt
}

func (s *Server) SetTunAttempt(attempt, maxAttempt int) {
	s.restartMu.Lock()
	defer s.restartMu.Unlock()
	s.tunAttempt = attempt
	s.tunMaxAttempt = maxAttempt
}

func (s *Server) ClearRestarting() {
	s.restartMu.Lock()
	defer s.restartMu.Unlock()
	s.restarting = false
	s.restartReadyAt = time.Time{}
	s.tunAttempt = 0
	s.tunMaxAttempt = 0
}

// IsRestarting возвращает true если sing-box сейчас восстанавливается после краша.
// Используется handleApply для предотвращения гонки между crash-recovery и apply.
func (s *Server) IsRestarting() bool {
	s.restartMu.RLock()
	defer s.restartMu.RUnlock()
	return s.restarting
}

// TriggerRestart программно запускает рестарт sing-box с уже готовым конфигом на диске.
// Используется applyTURNMode: конфиг уже записан с TURN override ДО этого вызова.
// TriggerApplyWithConfig НЕ перегенерирует конфиг — иначе TURN override был бы уничтожен.
// Возвращает ошибку если apply уже выполняется или tunHandlers не инициализированы.
func (s *Server) TriggerRestart(configPath string) error {
	if s.tunHandlers == nil {
		return fmt.Errorf("TunHandlers не инициализированы")
	}
	s.logger.Info("TriggerRestart: запуск перезапуска sing-box (конфиг: %s)", configPath)
	return s.tunHandlers.TriggerApplyWithConfig()
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	proxyConfig := s.config.ProxyManager.GetConfig()

	s.configMu.RLock()
	xrayMgr := s.config.XRayManager
	s.configMu.RUnlock()

	response := StatusResponse{
		ConfigPath: s.config.ConfigPath,
	}

	s.turnMu.RLock()
	response.TurnActive = s.turnActive
	response.TurnManual = s.turnManual
	s.turnMu.RUnlock()

	s.restartMu.RLock()
	restarting := s.restarting
	restartReadyAt := s.restartReadyAt
	tunAttempt := s.tunAttempt
	tunMaxAttempt := s.tunMaxAttempt
	s.restartMu.RUnlock()

	if xrayMgr == nil {
		response.XRay.Running = false
		response.XRay.Warming = true
		response.XRay.HealthStatus = "warming"
		if eta := wintun.EstimateReadyAt(); eta.After(time.Now()) {
			response.XRay.ReadyAt = eta.Unix()
		}
	} else if restarting {
		response.XRay.Running = false
		response.XRay.Warming = true
		response.XRay.HealthStatus = "restarting"
		if restartReadyAt.After(time.Now()) {
			response.XRay.ReadyAt = restartReadyAt.Unix()
		}
		response.XRay.TunAttempt = tunAttempt
		response.XRay.TunMaxAttempt = tunMaxAttempt
	} else {
		response.XRay.Running = xrayMgr.IsRunning()
		response.XRay.PID = xrayMgr.GetPID()
		response.XRay.Warming = false
		response.XRay.ReadyAt = 0

		// БАГ #3: получаем статус здоровья VLESS сервиса
		errorCount, errorRatePct, _ := xrayMgr.GetHealthStatus()
		response.XRay.ErrorCount = errorCount
		response.XRay.ErrorRatePct = errorRatePct

		if errorRatePct > 70 {
			response.XRay.HealthStatus = "unavailable"
		} else if errorRatePct > 30 {
			response.XRay.HealthStatus = "degraded"
		} else {
			response.XRay.HealthStatus = "healthy"
		}
	}
	response.Proxy.Enabled = s.config.ProxyManager.IsEnabled()
	response.Proxy.Address = proxyConfig.Address

	s.proxyEnabledAtMu.RLock()
	if response.Proxy.Enabled && !s.proxyEnabledAt.IsZero() {
		response.Proxy.UptimeS = int64(time.Since(s.proxyEnabledAt).Seconds())
	}
	s.proxyEnabledAtMu.RUnlock()

	s.respondJSON(w, http.StatusOK, response)
}

func (s *Server) handleProxyEnable(w http.ResponseWriter, r *http.Request) {
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
	s.proxyEnabledAtMu.Lock()
	s.proxyEnabledAt = time.Now()
	s.proxyEnabledAtMu.Unlock()

	switchClashMode(r.Context(), s.logger, "rule")
	s.respondJSON(w, http.StatusOK, MessageResponse{Message: "Прокси успешно включен", Success: true})
}

func (s *Server) handleProxyDisable(w http.ResponseWriter, r *http.Request) {
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
	s.proxyEnabledAtMu.Lock()
	s.proxyEnabledAt = time.Time{}
	s.proxyEnabledAtMu.Unlock()

	switchClashMode(r.Context(), s.logger, "direct")
	s.respondJSON(w, http.StatusOK, MessageResponse{Message: "Прокси успешно отключен", Success: true})
}

func (s *Server) handleProxyToggle(w http.ResponseWriter, r *http.Request) {
	s.proxyOpMu.Lock()
	defer s.proxyOpMu.Unlock()

	if s.config.ProxyManager.IsEnabled() {
		if err := s.config.ProxyManager.Disable(); err != nil {
			s.respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		switchClashMode(r.Context(), s.logger, "direct")
		s.respondJSON(w, http.StatusOK, MessageResponse{Message: "Прокси отключен", Success: true})
		return
	}
	if err := s.config.ProxyManager.Enable(proxy.Config{
		Address:  DefaultProxyAddress,
		Override: "<local>",
	}); err != nil {
		s.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.proxyEnabledAtMu.Lock()
	s.proxyEnabledAt = time.Now()
	s.proxyEnabledAtMu.Unlock()
	switchClashMode(r.Context(), s.logger, "rule")
	s.respondJSON(w, http.StatusOK, MessageResponse{Message: "Прокси включен", Success: true})
}

// switchClashMode переключает режим Clash API (rule/direct).
// Использует таймаут 2s чтобы не блокировать хендлер при недоступном Clash API.
func switchClashMode(ctx context.Context, log logger.Logger, mode string) {
	body := []byte(`{"mode":"` + mode + `"}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, clashAPIURL+"/configs", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		if log != nil {
			log.Debug("Clash API недоступен при смене режима на %q: %v", mode, err)
		}
		return
	}
	resp.Body.Close()
	if log != nil {
		log.Info("TUN режим переключён: %s", mode)
	}
}

// rateLimitMiddleware ограничивает частоту мутирующих запросов (POST/PUT/DELETE/PATCH).
// Защищает от быстрых повторных нажатий в UI и параллельных перезапусков sing-box.
// Лимит: 5 запросов в секунду глобально на все мутирующие эндпоинты.
// Если rateLimiter == nil (создан вручную без NewServer) — ограничений нет.
func (s *Server) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.rateLimiter != nil &&
			(r.Method == http.MethodPost || r.Method == http.MethodPut ||
				r.Method == http.MethodDelete || r.Method == http.MethodPatch) {
			if !s.rateLimiter.allow() {
				s.respondError(w, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	s.respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleQuit(w http.ResponseWriter, _ *http.Request) {
	s.respondJSON(w, http.StatusOK, MessageResponse{Message: "shutting down", Success: true})
	if s.config.QuitChan != nil {
		go func() {
			time.Sleep(100 * time.Millisecond)
			s.quitOnce.Do(func() { close(s.config.QuitChan) })
		}()
	}
}

func (s *Server) respondJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		s.logger.Error("Ошибка при кодировании JSON: %v", err)
	}
}

func (s *Server) respondError(w http.ResponseWriter, status int, message string) {
	s.respondJSON(w, status, ErrorResponse{Error: message})
}

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	allowed := map[string]bool{
		"http://localhost:8080": true,
		"http://127.0.0.1:8080": true,
		"app://":                true,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" || allowed[origin] {
			if origin != "" {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		} else {
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusForbidden)
				return
			}
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// maxBodyMiddleware ограничивает тело запроса 2 МБ для всех POST/PUT/PATCH запросов.
// Защищает от DoS через исчерпание памяти при огромных телах запросов.
// handleImport и handleGeositeDownload имеют собственные лимиты — middleware даёт
// дополнительный защитный слой.
func (s *Server) maxBodyMiddleware(next http.Handler) http.Handler {
	const maxBody = 2 << 20 // 2 MB
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch {
			r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) addSilentPath(path string) {
	s.silentMu.Lock()
	defer s.silentMu.Unlock()
	s.config.SilentPaths = append(s.config.SilentPaths, path)
	s.silentCache = nil
}

// loggingMiddleware логирует HTTP запросы.
func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.silentMu.RLock()
		cache := s.silentCache
		s.silentMu.RUnlock()

		if cache == nil {
			s.silentMu.Lock()
			if s.silentCache == nil {
				// ФИКС: Добавляем /api/events/clear в список тихих путей.
				// Без этого после очистки лога middleware записывает "POST /api/events/clear - 200"
				// в свежеочищенный лог, что ломает тесты на проверку пустоты лога.
				m := map[string]bool{
					"/api/status":           true,
					"/api/health":           true,
					"/api/tun/apply/status": true,
					"/api/events":           true,
					"/api/events/clear":     true, // Добавлено!
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

func (s *Server) recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				s.logger.Error("Паника в обработчике: %v", err)
				// A-5: логируем полный стек для диагностики реальных паник.
				s.logger.Error("Stack:\n%s", debug.Stack())
				s.respondError(w, http.StatusInternalServerError, "внутренняя ошибка сервера")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

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

func (s *Server) handleEventsClear(w http.ResponseWriter, _ *http.Request) {
	if s.config.EventLog != nil {
		s.config.EventLog.Clear()
	}
	s.respondJSON(w, http.StatusOK, MessageResponse{Message: "cleared", Success: true})
}

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}
