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

	"proxyclient/internal/eventlog"
	"proxyclient/internal/logger"
	"proxyclient/internal/proxy"
	"proxyclient/internal/xray"

	"github.com/gorilla/mux"
)

// DefaultProxyAddress адрес HTTP-прокси по умолчанию (sing-box http inbound)
const DefaultProxyAddress = "127.0.0.1:10807"

// Config конфигурация API сервера
type Config struct {
	ListenAddress string
	XRayManager   xray.Manager
	ProxyManager  proxy.Manager
	ConfigPath    string
	Logger        logger.Logger
	EventLog      *eventlog.Log // может быть nil — тогда /api/events недоступен
}

// Server HTTP API сервер
type Server struct {
	config     Config
	configMu   sync.RWMutex // защищает изменяемые поля config (XRayManager)
	proxyOpMu  sync.Mutex   // сериализует check+act операции над прокси (устраняет TOCTOU)
	router     *mux.Router
	httpServer *http.Server
	logger     logger.Logger
}

// StatusResponse ответ для /api/status
type StatusResponse struct {
	XRay struct {
		Running bool `json:"running"`
		PID     int  `json:"pid"`
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

// NewServer создаёт новый API сервер
func NewServer(cfg Config) *Server {
	s := &Server{
		config: cfg,
		logger: cfg.Logger,
		router: mux.NewRouter(),
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
	api.HandleFunc("/events", s.handleEvents).Methods("GET", "OPTIONS")
	api.HandleFunc("/events/clear", s.handleEventsClear).Methods("POST", "OPTIONS")
}

// FinalizeRoutes регистрирует статику — вызывать после всех других маршрутов
func (s *Server) FinalizeRoutes() {
	s.router.PathPrefix("/").Handler(staticHandler())
}

// Start запускает HTTP сервер
func (s *Server) Start(ctx context.Context) error {
	s.httpServer = &http.Server{
		Addr:         s.config.ListenAddress,
		Handler:      s.router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
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
		// Контекст отменён — корректно останавливаем сервер.
		// Ранее здесь был просто return nil, из-за чего ListenAndServe продолжал
		// работать: горутина и порт оставались занятыми до явного вызова Shutdown.
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
	// ErrServerClosed возникает при повторном вызове Shutdown — это нормально:
	// первый вызов происходит явно в main.go, второй — через defer cancel() → ctx.Done().
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("ошибка при остановке API сервера: %w", err)
	}
	s.logger.Info("API сервер остановлен")
	return nil
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
	response.XRay.Running = xrayMgr.IsRunning()
	response.XRay.PID = xrayMgr.GetPID()
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

// corsMiddleware добавляет CORS заголовки
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// loggingMiddleware логирует HTTP запросы.
// Частые polling-эндпоинты и статика не логируются — иначе флудят stdout
// вместе с логами sing-box и могут блокировать запись в буфер.
func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	// Эндпоинты которые поллятся каждую секунду — не логируем
	silentPaths := map[string]bool{
		"/api/status":           true,
		"/api/health":           true,
		"/api/tun/apply/status": true,
		"/api/events":           true,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if silentPaths[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		// Статические файлы тоже не логируем.
		// BUG FIX: используем strings.HasSuffix вместо ручных slice операций —
		// p[len(p)-3:] паникует если len(p) < 3 (например путь "/a").
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
