package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"strconv"
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

	tb.refill(time.Now())
	return tb.take()
}

func (tb *tokenBucket) refill(now time.Time) {
	elapsed := now.Sub(tb.lastRefill).Seconds()
	tb.tokens += elapsed * tb.refillRate
	if tb.tokens > tb.maxTokens {
		tb.tokens = tb.maxTokens
	}
	tb.lastRefill = now
}

func (tb *tokenBucket) take() bool {
	if tb.tokens >= 1.0 {
		tb.tokens--
		return true
	}
	return false
}

// DefaultProxyAddress адрес HTTP-прокси по умолчанию — алиас config.ProxyAddr.
const DefaultProxyAddress = config.ProxyAddr
const DefaultProxyOverride = "<local>;localhost;127.0.0.1;::1"

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

	silentMu        sync.RWMutex
	silentCache     map[string]bool
	tunHandlers     *TunHandlers
	serversHandlers *ServersHandlers
	reconnectMu     sync.Mutex
	reconnectCancel context.CancelFunc

	proxyEnabledAtMu sync.RWMutex
	proxyEnabledAt   time.Time

	// B-2: Proxy Guard состояние
	proxyGuardMu       sync.RWMutex
	proxyGuardEnabled  bool
	proxyGuardInterval time.Duration // защищён proxyGuardMu

	// B-10: автообновление geosite баз данных
	// БАГ 7: geoUpdaterMu защищает geoUpdater от гонки данных между
	// startBackground (SetGeoAutoUpdater) и HTTP-обработчиками (GetGeoAutoUpdater).
	geoUpdaterMu sync.RWMutex
	geoUpdater   *GeoAutoUpdater
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

// SetProxyGuardInterval сохраняет интервал Proxy Guard — нужен для StartProxyGuard из HTTP-хендлера.
// Устанавливается App при инициализации через finalizeStartup.
func (s *Server) SetProxyGuardInterval(d time.Duration) {
	s.proxyGuardMu.Lock()
	s.proxyGuardInterval = d
	s.proxyGuardMu.Unlock()
}

// StartProxyGuard запускает guard-горутину proxy.Manager.
func (s *Server) StartProxyGuard() error {
	s.proxyGuardMu.Lock()
	interval := s.proxyGuardInterval
	s.proxyGuardMu.Unlock()
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return s.config.ProxyManager.StartGuard(s.lifecycleCtx, interval)
}

// StopProxyGuard останавливает guard-горутину proxy.Manager.
func (s *Server) StopProxyGuard() {
	s.config.ProxyManager.StopGuard()
}

// B-10 / БАГ 7: SetGeoAutoUpdater регистрирует updater для управления через API настроек.
// Защищён geoUpdaterMu — записывается из горутины startBackground.
// Останавливает предыдущий updater если он ещё работает — предотвращает утечку горутин
// при повторном вызове (TUN recovery → finalizeStartup → SetGeoAutoUpdater).
func (s *Server) SetGeoAutoUpdater(g *GeoAutoUpdater) {
	// BUG-5 FIX: подключаем TriggerApply как callback — sing-box перезагружает
	// конфиг сразу после обновления geosite файлов, а не ждёт следующего ручного apply.
	s.geoUpdaterMu.Lock()
	if g != nil {
		g.SetOnUpdated(func() {
			if s.tunHandlers != nil {
				if err := s.tunHandlers.TriggerApply(); err != nil {
					s.logger.Warn("GeoAutoUpdater: TriggerApply после обновления geosite: %v", err)
				}
			}
		})
	}
	old := s.geoUpdater
	s.geoUpdater = g
	s.geoUpdaterMu.Unlock()
	// Stop вызывается вне мьютекса — он ждёт завершения горутины (blocking).
	// Держать мьютекс во время Stop() создало бы дедлок с GetGeoAutoUpdater.
	if old != nil && old != g && old.IsRunning() {
		old.Stop()
	}
}

// B-10 / БАГ 7: GetGeoAutoUpdater возвращает текущий updater (может быть nil).
// Защищён geoUpdaterMu — читается из HTTP-обработчиков параллельно с SetGeoAutoUpdater.
func (s *Server) GetGeoAutoUpdater() *GeoAutoUpdater {
	s.geoUpdaterMu.RLock()
	defer s.geoUpdaterMu.RUnlock()
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
		rateLimiter:  newTokenBucket(defaultMutationRatePerSecond),
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
	SetupImprovementRoutes(s)
	s.SetupGeoIPRoutes() // локальное определение страны без внешних запросов
	if s.config.SecretKeyPath != "" {
		s.serversHandlers = SetupServerRoutes(s, s.config.SecretKeyPath)
	}

	api := s.router.PathPrefix("/api").Subrouter()
	api.HandleFunc("/geosite", s.handleGeositeList).Methods("GET")
	api.HandleFunc("/geosite/download", s.handleGeositeDownload).Methods("POST")
	api.HandleFunc("/singbox-config", s.handleGetSingBoxConfig).Methods("GET", "OPTIONS")
	api.HandleFunc("/singbox-config", s.handleSetSingBoxConfig).Methods("POST", "OPTIONS")
	// B-8: Backup и restore endpoints
	api.HandleFunc("/backup", s.handleBackup).Methods("GET", "OPTIONS")
	api.HandleFunc("/backup/restore", s.handleBackupRestore).Methods("POST", "OPTIONS")

	// Иконки процессов — извлекаем из .exe через Shell API
	api.HandleFunc("/procicon", s.handleProcIcon).Methods("GET", "OPTIONS")

	s.addSilentPath("/api/stats")
	s.addSilentPath("/api/connections")
	s.addSilentPath("/api/servers")  // FIX 31: подавляем шумные логи при опросе списка серверов
	s.addSilentPath("/api/geoip")    // частые lookup-запросы для флагов стран
	s.addSilentPath("/api/procicon") // иконки процессов — частые запросы, не нужно логировать
	s.addSilentPath("/api/traffic/by-process")
	s.addSilentPath("/api/stats/total")
	s.addSilentPath("/api/connections/history")
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
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.logger.Info("API сервер запущен на %s", s.config.ListenAddress)
		if err := s.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			select {
			case errChan <- err:
			default:
			}
		}
	}()

	select {
	case err := <-errChan:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.httpServer.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(shutdownCtx.Err(), context.DeadlineExceeded) {
				s.logger.Warn("API сервер не завершился graceful за 5s — закрываем активные соединения")
				closeErr := s.httpServer.Close()
				wg.Wait()
				return closeErr
			}
			wg.Wait()
			return err
		}
		wg.Wait()
		return nil
	}
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.StopPeriodicReconnect()
	if s.serversHandlers != nil {
		s.serversHandlers.Shutdown()
	}
	if s.httpServer == nil {
		return nil
	}
	s.logger.Info("Остановка API сервера...")
	err := s.httpServer.Shutdown(ctx)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			s.logger.Warn("API сервер не завершился graceful до таймаута — закрываем активные соединения")
			if closeErr := s.httpServer.Close(); closeErr != nil && !errors.Is(closeErr, http.ErrServerClosed) {
				return fmt.Errorf("ошибка принудительной остановки API сервера: %w", closeErr)
			}
			return nil
		}
		return fmt.Errorf("ошибка при остановке API сервера: %w", err)
	}
	return nil
}

func (s *Server) StartPeriodicReconnect(interval time.Duration) {
	if interval <= 0 {
		return
	}
	s.reconnectMu.Lock()
	if s.reconnectCancel != nil {
		s.reconnectCancel()
	}
	ctx, cancel := context.WithCancel(s.lifecycleCtx)
	s.reconnectCancel = cancel
	s.reconnectMu.Unlock()
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if s.tunHandlers != nil {
					s.logger.Debug("PeriodicReconnect: ротация сессии")
					_ = s.tunHandlers.TriggerApply()
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (s *Server) StopPeriodicReconnect() {
	s.reconnectMu.Lock()
	cancel := s.reconnectCancel
	s.reconnectCancel = nil
	s.reconnectMu.Unlock()
	if cancel != nil {
		cancel()
	}
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
	s.restarting = false
	s.restartReadyAt = time.Time{}
	s.tunAttempt = 0
	s.tunMaxAttempt = 0
	s.restartMu.Unlock()

	s.DrainQueuedApply()
}

// IsRestarting возвращает true если sing-box сейчас восстанавливается после краша.
// Используется handleApply для предотвращения гонки между crash-recovery и apply.
func (s *Server) IsRestarting() bool {
	s.restartMu.RLock()
	defer s.restartMu.RUnlock()
	return s.restarting
}

// DrainQueuedApply запускает отложенное применение правил, если оно было поставлено
// в очередь во время первичного старта или TUN crash-recovery.
func (s *Server) DrainQueuedApply() {
	if s.tunHandlers == nil {
		return
	}
	go s.tunHandlers.drainQueuedApply()
}

func (s *Server) IsApplyBusy() bool {
	if s.tunHandlers == nil {
		return false
	}
	return s.tunHandlers.IsApplyBusy()
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

func (s *Server) TriggerApply() error {
	if s.tunHandlers == nil {
		return fmt.Errorf("TunHandlers не инициализированы")
	}
	return s.tunHandlers.TriggerApply()
}

func (s *Server) AutoConnect() {
	if s.serversHandlers != nil {
		s.serversHandlers.AutoConnect()
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
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

		// БАГ #3: получаем статус здоровья VLESS сервиса.
		// BUG-NEW-6 FIX: errorRatePct теперь = (errorCount/thresholdCount)*100 —
		// насыщенность буфера алертов, а не rate от connectionCount (который всегда был 0).
		errorCount, errorRatePct, wouldAlert := xrayMgr.GetHealthStatus()
		response.XRay.ErrorCount = errorCount
		response.XRay.ErrorRatePct = errorRatePct

		if wouldAlert && errorRatePct > 150 {
			response.XRay.HealthStatus = "unavailable"
		} else if wouldAlert {
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

// rateLimitMiddleware ограничивает частоту мутирующих запросов (POST/PUT/DELETE/PATCH).
// Защищает от быстрых повторных нажатий в UI и параллельных перезапусков sing-box.
// Лимит: 5 запросов в секунду глобально на все мутирующие эндпоинты.
// Если rateLimiter == nil (создан вручную без NewServer) — ограничений нет.
func (s *Server) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.rateLimiter != nil && isMutationMethod(r.Method) {
			if !s.rateLimiter.allow() {
				s.respondError(w, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	s.respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleQuit(w http.ResponseWriter, _ *http.Request) {
	s.respondJSON(w, http.StatusOK, MessageResponse{Message: "shutting down", Success: true})
	if s.config.QuitChan != nil {
		go func() {
			time.Sleep(quitSignalDelay)
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if isAllowedCORSOrigin(origin) {
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isRequestBodyLimitedMethod(r.Method) {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
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
		if s.getSilentPathCache()[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}

		if isStaticAssetPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rw, r)
		elapsed := time.Since(start)
		// Логируем только медленные запросы (>200ms) подробно, остальные — компактно
		path := r.URL.Path
		if q := r.URL.RawQuery; q != "" {
			path += "?" + q
		}
		if elapsed > slowRequestThreshold {
			s.logger.Info("→ %s %s  %d  %v ⚠", r.Method, path, rw.statusCode, elapsed.Round(time.Millisecond))
		} else {
			s.logger.Info("→ %s %s  %d", r.Method, path, rw.statusCode)
		}
	})
}

func (s *Server) recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		defer func() {
			if err := recover(); err != nil {
				s.logger.Error("Паника в обработчике: %v", err)
				// A-5: логируем полный стек для диагностики реальных паник.
				s.logger.Error("Stack:\n%s", debug.Stack())
				// Отправляем 500 только если заголовки ещё не были записаны.
				// Повторный WriteHeader после уже начатого ответа вызывает
				// "superfluous response.WriteHeader call" и портит ответ.
				if !rw.wroteHeader {
					s.respondError(w, http.StatusInternalServerError, "внутренняя ошибка сервера")
				}
			}
		}()
		next.ServeHTTP(rw, r)
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
	statusCode  int
	wroteHeader bool
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.wroteHeader = true
	rw.ResponseWriter.WriteHeader(code)
}
