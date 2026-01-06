package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"proxyclient/internal/logger"
	"proxyclient/internal/proxy"
	"proxyclient/internal/xray"

	"github.com/gorilla/mux"
)

// Config конфигурация API сервера
type Config struct {
	ListenAddress string
	XRayManager   xray.Manager
	ProxyManager  proxy.Manager
	ConfigPath    string
	Logger        logger.Logger
}

// Server HTTP API сервер
type Server struct {
	config     Config
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

// setupRoutes настраивает маршруты
func (s *Server) setupRoutes() {
	// Middleware для логирования
	s.router.Use(s.loggingMiddleware)
	s.router.Use(s.recoveryMiddleware)

	// API endpoints
	api := s.router.PathPrefix("/api").Subrouter()
	api.HandleFunc("/status", s.handleStatus).Methods("GET")
	api.HandleFunc("/proxy/enable", s.handleProxyEnable).Methods("POST")
	api.HandleFunc("/proxy/disable", s.handleProxyDisable).Methods("POST")
	api.HandleFunc("/health", s.handleHealth).Methods("GET")
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
		return nil
	}
}

// Shutdown корректно останавливает сервер
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpServer == nil {
		return nil
	}

	s.logger.Info("Остановка API сервера...")
	if err := s.httpServer.Shutdown(ctx); err != nil {
		return fmt.Errorf("ошибка при остановке API сервера: %w", err)
	}

	s.logger.Info("API сервер остановлен")
	return nil
}

// handleStatus обрабатывает GET /api/status
func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	proxyConfig := s.config.ProxyManager.GetConfig()

	response := StatusResponse{
		ConfigPath: s.config.ConfigPath,
	}

	response.XRay.Running = s.config.XRayManager.IsRunning()
	response.XRay.PID = s.config.XRayManager.GetPID()

	response.Proxy.Enabled = s.config.ProxyManager.IsEnabled()
	response.Proxy.Address = proxyConfig.Address

	s.respondJSON(w, http.StatusOK, response)
}

// handleProxyEnable обрабатывает POST /api/proxy/enable
func (s *Server) handleProxyEnable(w http.ResponseWriter, _ *http.Request) {
	if s.config.ProxyManager.IsEnabled() {
		s.respondError(w, http.StatusBadRequest, "прокси уже включен")
		return
	}

	proxyConfig := proxy.Config{
		Address:  "127.0.0.1:10807",
		Override: "<local>",
	}

	if err := s.config.ProxyManager.Enable(proxyConfig); err != nil {
		s.logger.Error("Не удалось включить прокси: %v", err)
		s.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.respondJSON(w, http.StatusOK, MessageResponse{
		Message: "Прокси успешно включен",
	})
}

// handleProxyDisable обрабатывает POST /api/proxy/disable
func (s *Server) handleProxyDisable(w http.ResponseWriter, _ *http.Request) {
	if !s.config.ProxyManager.IsEnabled() {
		s.respondError(w, http.StatusBadRequest, "прокси уже отключен")
		return
	}

	if err := s.config.ProxyManager.Disable(); err != nil {
		s.logger.Error("Не удалось отключить прокси: %v", err)
		s.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.respondJSON(w, http.StatusOK, MessageResponse{
		Message: "Прокси успешно отключен",
	})
}

// handleHealth обрабатывает GET /api/health
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	s.respondJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
	})
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

// loggingMiddleware логирует HTTP запросы
func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Создаём ResponseWriter для захвата статус кода
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(rw, r)

		duration := time.Since(start)
		s.logger.Info(
			"%s %s - %d (%v)",
			r.Method,
			r.URL.Path,
			rw.statusCode,
			duration,
		)
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

// responseWriter обёртка для захвата статус кода
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}
