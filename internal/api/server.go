package api

import (
	"bytes"
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

	restartMu      sync.RWMutex
	restarting     bool
	restartReadyAt time.Time
	tunAttempt     int
	tunMaxAttempt  int

	silentMu    sync.RWMutex
	silentCache map[string]bool
	tunHandlers *TunHandlers
}

// StatusResponse ответ для /api/status
type StatusResponse struct {
	XRay struct {
		Running       bool  `json:"running"`
		PID           int   `json:"pid"`
		Warming       bool  `json:"warming"`
		ReadyAt       int64 `json:"ready_at"`
		TunAttempt    int   `json:"tun_attempt"`
		TunMaxAttempt int   `json:"tun_max_attempt"`
	} `json:"xray"`
	Proxy struct {
		Enabled bool   `json:"enabled"`
		Address string `json:"address"`
	} `json:"proxy"`
	ConfigPath string `json:"config_path"`
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
	}
	s.setupRoutes()
	return s
}

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
	tunAttempt := s.tunAttempt
	tunMaxAttempt := s.tunMaxAttempt
	s.restartMu.RUnlock()

	if xrayMgr == nil {
		response.XRay.Running = false
		response.XRay.Warming = true
		if eta := wintun.EstimateReadyAt(); eta.After(time.Now()) {
			response.XRay.ReadyAt = eta.Unix()
		}
	} else if restarting {
		response.XRay.Running = false
		response.XRay.Warming = true
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
	}
	response.Proxy.Enabled = s.config.ProxyManager.IsEnabled()
	response.Proxy.Address = proxyConfig.Address

	s.respondJSON(w, http.StatusOK, response)
}

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
	switchClashMode(s.logger, "rule")
	s.respondJSON(w, http.StatusOK, MessageResponse{Message: "Прокси успешно включен", Success: true})
}

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
	switchClashMode(s.logger, "direct")
	s.respondJSON(w, http.StatusOK, MessageResponse{Message: "Прокси успешно отключен", Success: true})
}

func (s *Server) handleProxyToggle(w http.ResponseWriter, _ *http.Request) {
	s.proxyOpMu.Lock()
	defer s.proxyOpMu.Unlock()

	if s.config.ProxyManager.IsEnabled() {
		if err := s.config.ProxyManager.Disable(); err != nil {
			s.respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		switchClashMode(s.logger, "direct")
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
	switchClashMode(s.logger, "rule")
	s.respondJSON(w, http.StatusOK, MessageResponse{Message: "Прокси включен", Success: true})
}

func switchClashMode(log logger.Logger, mode string) {
	body := []byte(`{"mode":"` + mode + `"}`)
	req, err := http.NewRequest(http.MethodPatch, config.ClashAPIBase+"/configs", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
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
