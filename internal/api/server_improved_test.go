//go:build windows
// +build windows

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"proxyclient/internal/eventlog"
	"proxyclient/internal/logger"
	"proxyclient/internal/proxy"

	"github.com/gorilla/mux"
)

// ── CORS Middleware Tests ────────────────────────────────────────────────────────

func TestCORSMiddleware_AllowsLocalhost(t *testing.T) {
	allowedOrigins := []string{
		"http://localhost:8080",
		"http://127.0.0.1:8080",
	}

	for _, origin := range allowedOrigins {
		t.Run(origin, func(t *testing.T) {
			log := logger.New(logger.LevelInfo)
			s := &Server{
				logger: log,
				router: mux.NewRouter(),
			}
			s.router.Use(s.corsMiddleware)
			s.router.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})

			req := httptest.NewRequest("GET", "/test", nil)
			req.Header.Set("Origin", origin)
			rec := httptest.NewRecorder()

			s.router.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
			}

			if rec.Header().Get("Access-Control-Allow-Origin") != origin {
				t.Errorf("Allow-Origin = %q, want %q", rec.Header().Get("Access-Control-Allow-Origin"), origin)
			}
		})
	}
}

func TestCORSMiddleware_AllowsAppScheme(t *testing.T) {
	log := logger.New(logger.LevelInfo)
	s := &Server{
		logger: log,
		router: mux.NewRouter(),
	}
	s.router.Use(s.corsMiddleware)
	s.router.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Origin", "app://")
	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestCORSMiddleware_BlocksForeignOrigin(t *testing.T) {
	log := logger.New(logger.LevelInfo)
	s := &Server{
		logger: log,
		router: mux.NewRouter(),
	}
	s.router.Use(s.corsMiddleware)
	s.router.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Test preflight (OPTIONS)
	req := httptest.NewRequest("OPTIONS", "/test", nil)
	req.Header.Set("Origin", "https://evil.com")
	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("Preflight Status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestCORSMiddleware_AllowsNoOrigin(t *testing.T) {
	log := logger.New(logger.LevelInfo)
	s := &Server{
		logger: log,
		router: mux.NewRouter(),
	}
	s.router.Use(s.corsMiddleware)
	s.router.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Request without Origin header (curl, Postman)
	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestCORSMiddleware_SetsAllowMethods(t *testing.T) {
	log := logger.New(logger.LevelInfo)
	s := &Server{
		logger: log,
		router: mux.NewRouter(),
	}
	s.router.Use(s.corsMiddleware)
	s.router.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("OPTIONS", "/test", nil)
	req.Header.Set("Origin", "http://localhost:8080")
	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	methods := rec.Header().Get("Access-Control-Allow-Methods")
	if methods == "" {
		t.Error("Allow-Methods header is empty")
	}

	expectedMethods := []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"}
	for _, m := range expectedMethods {
		if !strings.Contains(methods, m) {
			t.Errorf("Allow-Methods = %q, should contain %q", methods, m)
		}
	}
}

// ── Recovery Middleware Tests ────────────────────────────────────────────────────

func TestRecoveryMiddleware_RecoversFromPanic(t *testing.T) {
	log := logger.New(logger.LevelInfo)
	s := &Server{
		logger: log,
		router: mux.NewRouter(),
	}
	s.router.Use(s.recoveryMiddleware)
	s.router.HandleFunc("/panic", func(w http.ResponseWriter, r *http.Request) {
		panic("test panic")
	})

	req := httptest.NewRequest("GET", "/panic", nil)
	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}

	var resp ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if !strings.Contains(resp.Error, "внутренняя ошибка") {
		t.Errorf("Error = %q, should contain 'внутренняя ошибка'", resp.Error)
	}
}

func TestRecoveryMiddleware_PassesNormalRequest(t *testing.T) {
	log := logger.New(logger.LevelInfo)
	s := &Server{
		logger: log,
		router: mux.NewRouter(),
	}
	s.router.Use(s.recoveryMiddleware)
	s.router.HandleFunc("/normal", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	req := httptest.NewRequest("GET", "/normal", nil)
	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}
}

// ── Logging Middleware Tests ─────────────────────────────────────────────────────

func TestLoggingMiddleware_SkipsSilentPaths(t *testing.T) {
	log := logger.New(logger.LevelInfo)
	s := &Server{
		logger: log,
		router: mux.NewRouter(),
		config: Config{
			SilentPaths: []string{"/api/silent"},
		},
	}
	s.router.Use(s.loggingMiddleware)

	called := false
	s.router.HandleFunc("/api/silent", func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/api/silent", nil)
	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	if !called {
		t.Error("Handler was not called")
	}
}

func TestLoggingMiddleware_LogsNonSilentPaths(t *testing.T) {
	log := logger.New(logger.LevelInfo)
	s := &Server{
		logger: log,
		router: mux.NewRouter(),
	}
	s.router.Use(s.loggingMiddleware)

	s.router.HandleFunc("/api/noisy", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/api/noisy", nil)
	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestLoggingMiddleware_SkipsStaticFiles(t *testing.T) {
	log := logger.New(logger.LevelInfo)
	s := &Server{
		logger: log,
		router: mux.NewRouter(),
	}
	s.router.Use(s.loggingMiddleware)

	staticPaths := []string{"/app.js", "/style.css", "/favicon.ico", "/index.html"}

	for _, path := range staticPaths {
		t.Run(path, func(t *testing.T) {
			s.router.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})

			req := httptest.NewRequest("GET", path, nil)
			rec := httptest.NewRecorder()

			s.router.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
			}
		})
	}
}

// ── Status Response Tests ────────────────────────────────────────────────────────

func TestHandleStatus_ReturnsJSON(t *testing.T) {
	log := logger.New(logger.LevelInfo)
	evLog := eventlog.New(50)
	proxyMgr := &mockProxyManager{enabled: false, address: "127.0.0.1:8080"}

	s := &Server{
		logger: log,
		router: mux.NewRouter(),
		config: Config{
			Logger:       log,
			EventLog:     evLog,
			ProxyManager: proxyMgr,
			ConfigPath:   "test.json",
		},
	}
	s.setupRoutes()

	req := httptest.NewRequest("GET", "/api/status", nil)
	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp StatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp.ConfigPath != "test.json" {
		t.Errorf("ConfigPath = %q, want %q", resp.ConfigPath, "test.json")
	}
}

// ── Events Handler Tests ─────────────────────────────────────────────────────────

func TestHandleEvents_ReturnsEmpty_WhenNoEventLog(t *testing.T) {
	log := logger.New(logger.LevelInfo)

	s := &Server{
		logger: log,
		router: mux.NewRouter(),
		config: Config{
			Logger:   log,
			EventLog: nil, // No event log
		},
	}
	s.setupRoutes()

	req := httptest.NewRequest("GET", "/api/events", nil)
	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp["events"] == nil {
		t.Error("events field should exist")
	}
}

func TestHandleEvents_ReturnsEvents(t *testing.T) {
	log := logger.New(logger.LevelInfo)
	evLog := eventlog.New(50)
	evLog.Add(eventlog.LevelInfo, "test", "Test message")

	s := &Server{
		logger: log,
		router: mux.NewRouter(),
		config: Config{
			Logger:   log,
			EventLog: evLog,
		},
	}
	s.setupRoutes()

	req := httptest.NewRequest("GET", "/api/events", nil)
	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	events, ok := resp["events"].([]interface{})
	if !ok {
		t.Fatal("events is not an array")
	}

	if len(events) != 1 {
		t.Errorf("Expected 1 event, got %d", len(events))
	}
}

func TestHandleEvents_SinceParameter(t *testing.T) {
	log := logger.New(logger.LevelInfo)
	evLog := eventlog.New(50)
	evLog.Add(eventlog.LevelInfo, "test", "First")
	id := evLog.GetLatestID()
	evLog.Add(eventlog.LevelInfo, "test", "Second")

	s := &Server{
		logger: log,
		router: mux.NewRouter(),
		config: Config{
			Logger:   log,
			EventLog: evLog,
		},
	}
	s.setupRoutes()

	req := httptest.NewRequest("GET", fmt.Sprintf("/api/events?since=%d", id), nil)
	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}
}

// ── Events Clear Handler Tests ───────────────────────────────────────────────────

func TestHandleEventsClear_ClearsLog(t *testing.T) {
	log := logger.New(logger.LevelInfo)
	evLog := eventlog.New(50)
	evLog.Add(eventlog.LevelInfo, "test", "Test")

	s := &Server{
		logger: log,
		router: mux.NewRouter(),
		config: Config{
			Logger:   log,
			EventLog: evLog,
		},
	}
	s.setupRoutes()

	req := httptest.NewRequest("POST", "/api/events/clear", nil)
	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}

	// Verify log is cleared
	if evLog.GetLatestID() != 0 {
		t.Error("Event log should be empty after clear")
	}
}

func TestHandleEventsClear_NoPanic_WhenNilEventLog(t *testing.T) {
	log := logger.New(logger.LevelInfo)

	s := &Server{
		logger: log,
		router: mux.NewRouter(),
		config: Config{
			Logger:   log,
			EventLog: nil,
		},
	}
	s.setupRoutes()

	req := httptest.NewRequest("POST", "/api/events/clear", nil)
	rec := httptest.NewRecorder()

	// Should not panic
	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}
}

// ── Health Handler Tests ─────────────────────────────────────────────────────────

func TestHandleHealth_ReturnsOK(t *testing.T) {
	log := logger.New(logger.LevelInfo)

	s := &Server{
		logger: log,
		router: mux.NewRouter(),
		config: Config{
			Logger: log,
		},
	}
	s.setupRoutes()

	req := httptest.NewRequest("GET", "/api/health", nil)
	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp["status"] != "ok" {
		t.Errorf("status = %q, want %q", resp["status"], "ok")
	}
}

// ── Quit Handler Tests ───────────────────────────────────────────────────────────

func TestHandleQuit_ClosesQuitChan(t *testing.T) {
	log := logger.New(logger.LevelInfo)
	quitChan := make(chan struct{})

	s := &Server{
		logger: log,
		router: mux.NewRouter(),
		config: Config{
			Logger:   log,
			QuitChan: quitChan,
		},
	}
	s.setupRoutes()

	req := httptest.NewRequest("POST", "/api/quit", nil)
	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}

	// Wait for quit channel to close
	select {
	case <-quitChan:
		// Success
	case <-time.After(time.Second):
		t.Error("Quit channel was not closed")
	}
}

func TestHandleQuit_NoPanic_WhenNilQuitChan(t *testing.T) {
	log := logger.New(logger.LevelInfo)

	s := &Server{
		logger: log,
		router: mux.NewRouter(),
		config: Config{
			Logger:   log,
			QuitChan: nil,
		},
	}
	s.setupRoutes()

	req := httptest.NewRequest("POST", "/api/quit", nil)
	rec := httptest.NewRecorder()

	// Should not panic
	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestHandleQuit_OnlyOnce(t *testing.T) {
	log := logger.New(logger.LevelInfo)
	quitChan := make(chan struct{})

	s := &Server{
		logger: log,
		router: mux.NewRouter(),
		config: Config{
			Logger:   log,
			QuitChan: quitChan,
		},
	}
	s.setupRoutes()

	// Call quit twice
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("POST", "/api/quit", nil)
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, req)
	}

	// Channel should close only once (no panic from double close)
	select {
	case <-quitChan:
		// Success
	case <-time.After(time.Second):
		t.Error("Quit channel was not closed")
	}
}

// ── Proxy Enable/Disable Handler Tests ───────────────────────────────────────────

func TestHandleProxyEnable_Returns400_WhenAlreadyEnabled(t *testing.T) {
	log := logger.New(logger.LevelInfo)
	proxyMgr := &mockProxyManager{enabled: true}

	s := &Server{
		logger: log,
		router: mux.NewRouter(),
		config: Config{
			Logger:       log,
			ProxyManager: proxyMgr,
		},
	}
	s.setupRoutes()

	req := httptest.NewRequest("POST", "/api/proxy/enable", nil)
	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleProxyDisable_Returns400_WhenAlreadyDisabled(t *testing.T) {
	log := logger.New(logger.LevelInfo)
	proxyMgr := &mockProxyManager{enabled: false}

	s := &Server{
		logger: log,
		router: mux.NewRouter(),
		config: Config{
			Logger:       log,
			ProxyManager: proxyMgr,
		},
	}
	s.setupRoutes()

	req := httptest.NewRequest("POST", "/api/proxy/disable", nil)
	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// ── Server Lifecycle Tests ───────────────────────────────────────────────────────

func TestNewServer_CreatesRouter(t *testing.T) {
	log := logger.New(logger.LevelInfo)

	s := NewServer(Config{
		Logger:        log,
		ListenAddress: "127.0.0.1:0",
	}, context.Background())

	if s == nil {
		t.Fatal("NewServer returned nil")
	}

	if s.router == nil {
		t.Error("Router is nil")
	}
}

func TestNewServer_UsesBackgroundContext_WhenNil(t *testing.T) {
	log := logger.New(logger.LevelInfo)

	s := NewServer(Config{
		Logger: log,
	}, nil)

	if s.lifecycleCtx == nil {
		t.Error("lifecycleCtx should not be nil")
	}
}

func TestSetXRayManager_UpdatesManager(t *testing.T) {
	log := logger.New(logger.LevelInfo)

	s := &Server{
		logger: log,
		config: Config{
			Logger: log,
		},
	}

	// Initially nil
	if s.GetXRayManager() != nil {
		t.Error("XRayManager should initially be nil")
	}

	// Set manager
	mockMgr := &mockXRayManager{running: true}
	s.SetXRayManager(mockMgr)

	if s.GetXRayManager() != mockMgr {
		t.Error("XRayManager not updated")
	}
}

func TestIsWarming_ReturnsTrue_WhenNoManager(t *testing.T) {
	log := logger.New(logger.LevelInfo)

	s := &Server{
		logger: log,
		config: Config{
			Logger: log,
		},
	}

	if !s.IsWarming() {
		t.Error("IsWarming should return true when no XRayManager")
	}
}

// ── Restart State Tests ──────────────────────────────────────────────────────────

func TestSetRestarting_SetsState(t *testing.T) {
	log := logger.New(logger.LevelInfo)

	s := &Server{
		logger: log,
	}

	readyAt := time.Now().Add(10 * time.Second)
	s.SetRestarting(readyAt)

	if !s.restarting {
		t.Error("restarting should be true")
	}

	if !s.restartReadyAt.Equal(readyAt) {
		t.Error("restartReadyAt not set correctly")
	}
}

func TestClearRestarting_ResetsState(t *testing.T) {
	log := logger.New(logger.LevelInfo)

	s := &Server{
		logger:      log,
		restarting:  true,
		tunAttempt:  3,
		tunMaxAttempt: 5,
	}

	s.ClearRestarting()

	if s.restarting {
		t.Error("restarting should be false")
	}

	if s.tunAttempt != 0 {
		t.Error("tunAttempt should be 0")
	}
}

func TestSetTunAttempt_UpdatesCounters(t *testing.T) {
	log := logger.New(logger.LevelInfo)

	s := &Server{
		logger: log,
	}

	s.SetTunAttempt(2, 5)

	if s.tunAttempt != 2 {
		t.Errorf("tunAttempt = %d, want 2", s.tunAttempt)
	}

	if s.tunMaxAttempt != 5 {
		t.Errorf("tunMaxAttempt = %d, want 5", s.tunMaxAttempt)
	}
}

// ── Mock Types ───────────────────────────────────────────────────────────────────

type mockProxyManager struct {
	enabled bool
	address string
	err     error
}

func (m *mockProxyManager) Enable(cfg proxy.Config) error {
	if m.err != nil {
		return m.err
	}
	m.enabled = true
	m.address = cfg.Address
	return nil
}

func (m *mockProxyManager) Disable() error {
	if m.err != nil {
		return m.err
	}
	m.enabled = false
	return nil
}

func (m *mockProxyManager) IsEnabled() bool {
	return m.enabled
}

func (m *mockProxyManager) GetConfig() proxy.Config {
	return proxy.Config{Address: m.address}
}

type mockXRayManager struct {
	running bool
	pid     int
	err     error
}

func (m *mockXRayManager) Start() error                          { return m.err }
func (m *mockXRayManager) StartAfterManualCleanup() error        { return m.err }
func (m *mockXRayManager) Stop() error                           { return m.err }
func (m *mockXRayManager) IsRunning() bool                       { return m.running }
func (m *mockXRayManager) GetPID() int                           { return m.pid }
func (m *mockXRayManager) Wait() error                           { return m.err }
func (m *mockXRayManager) LastOutput() string                    { return "" }
func (m *mockXRayManager) Uptime() time.Duration                 { return 0 }

// ── Response Writer Tests ────────────────────────────────────────────────────────

func TestResponseWriter_CapturesStatusCode(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := &responseWriter{ResponseWriter: rec, statusCode: http.StatusOK}

	rw.WriteHeader(http.StatusCreated)

	if rw.statusCode != http.StatusCreated {
		t.Errorf("statusCode = %d, want %d", rw.statusCode, http.StatusCreated)
	}
}

// ── SwitchClashMode Tests ────────────────────────────────────────────────────────

func TestSwitchClashMode_DoesNotPanic_OnError(t *testing.T) {
	log := logger.New(logger.LevelInfo)

	// This should not panic even when Clash API is not available
	switchClashMode(log, "rule")
	switchClashMode(log, "direct")
}

func TestSwitchClashMode_HandlesNilLogger(t *testing.T) {
	// Should not panic with nil logger
	switchClashMode(nil, "rule")
}

// ── respondJSON Tests ────────────────────────────────────────────────────────────

func TestRespondJSON_SetsContentType(t *testing.T) {
	log := logger.New(logger.LevelInfo)
	s := &Server{logger: log}

	rec := httptest.NewRecorder()
	s.respondJSON(rec, http.StatusOK, map[string]string{"test": "value"})

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/json")
	}
}

func TestRespondError_ReturnsCorrectFormat(t *testing.T) {
	log := logger.New(logger.LevelInfo)
	s := &Server{logger: log}

	rec := httptest.NewRecorder()
	s.respondError(rec, http.StatusBadRequest, "test error")

	var resp ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp.Error != "test error" {
		t.Errorf("Error = %q, want %q", resp.Error, "test error")
	}
}

// ── addSilentPath Tests ──────────────────────────────────────────────────────────

func TestAddSilentPath_InvalidatesCache(t *testing.T) {
	log := logger.New(logger.LevelInfo)
	s := &Server{
		logger:      log,
		silentCache: map[string]bool{"/api/status": true},
	}

	s.addSilentPath("/api/new")

	// Cache should be invalidated
	if s.silentCache != nil {
		t.Error("silentCache should be nil after addSilentPath")
	}
}

// ── Fuzz Tests ───────────────────────────────────────────────────────────────────

func FuzzCORSMiddleware(f *testing.F) {
	seeds := []string{
		"http://localhost:8080",
		"https://evil.com",
		"",
		"app://",
		"http://127.0.0.1:8080",
		"https://malicious.site",
	}

	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, origin string) {
		log := logger.New(logger.LevelInfo)
		s := &Server{
			logger: log,
			router: mux.NewRouter(),
		}
		s.router.Use(s.corsMiddleware)
		s.router.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})

		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()

		s.router.ServeHTTP(rec, req)

		// Should always return some status code
		_ = rec.Code
	})
}

func FuzzHandleEventsSince(f *testing.F) {
	seeds := []string{"", "0", "1", "abc", "-1", "999999"}

	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, since string) {
		log := logger.New(logger.LevelInfo)
		evLog := eventlog.New(50)

		s := &Server{
			logger: log,
			router: mux.NewRouter(),
			config: Config{
				Logger:   log,
				EventLog: evLog,
			},
		}
		s.setupRoutes()

		url := "/api/events"
		if since != "" {
			url += "?since=" + since
		}

		req := httptest.NewRequest("GET", url, nil)
		rec := httptest.NewRecorder()

		s.router.ServeHTTP(rec, req)

		// Should always return OK
		if rec.Code != http.StatusOK {
			t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
		}
	})
}

// ── Benchmark Tests ──────────────────────────────────────────────────────────────

func BenchmarkCORSMiddleware(b *testing.B) {
	log := logger.New(logger.LevelInfo)
	s := &Server{
		logger: log,
		router: mux.NewRouter(),
	}
	s.router.Use(s.corsMiddleware)
	s.router.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Origin", "http://localhost:8080")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, req)
	}
}

func BenchmarkHandleStatus(b *testing.B) {
	log := logger.New(logger.LevelInfo)
	evLog := eventlog.New(50)
	proxyMgr := &mockProxyManager{}

	s := &Server{
		logger: log,
		router: mux.NewRouter(),
		config: Config{
			Logger:       log,
			EventLog:     evLog,
			ProxyManager: proxyMgr,
		},
	}
	s.setupRoutes()

	req := httptest.NewRequest("GET", "/api/status", nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, req)
	}
}

func BenchmarkLoggingMiddleware(b *testing.B) {
	log := logger.New(logger.LevelInfo)
	s := &Server{
		logger: log,
		router: mux.NewRouter(),
	}
	s.router.Use(s.loggingMiddleware)
	s.router.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/test", nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, req)
	}
}

// ── Table-driven tests ───────────────────────────────────────────────────────────

func TestCORSMiddleware_Table(t *testing.T) {
	tests := []struct {
		name           string
		origin         string
		expectedStatus int
		shouldHaveCORS bool
	}{
		{"localhost", "http://localhost:8080", http.StatusOK, true},
		{"127.0.0.1", "http://127.0.0.1:8080", http.StatusOK, true},
		{"app scheme", "app://", http.StatusOK, true},
		{"no origin", "", http.StatusOK, false},
		{"evil.com OPTIONS", "https://evil.com", http.StatusForbidden, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			log := logger.New(logger.LevelInfo)
			s := &Server{
				logger: log,
				router: mux.NewRouter(),
			}
			s.router.Use(s.corsMiddleware)
			s.router.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})

			method := "GET"
			if strings.Contains(tc.name, "OPTIONS") {
				method = "OPTIONS"
			}

			req := httptest.NewRequest(method, "/test", nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			rec := httptest.NewRecorder()

			s.router.ServeHTTP(rec, req)

			if rec.Code != tc.expectedStatus {
				t.Errorf("Status = %d, want %d", rec.Code, tc.expectedStatus)
			}

			hasCORS := rec.Header().Get("Access-Control-Allow-Origin") != ""
			if hasCORS != tc.shouldHaveCORS {
				t.Errorf("Has CORS header = %v, want %v", hasCORS, tc.shouldHaveCORS)
			}
		})
	}
}

// ── Integration test ─────────────────────────────────────────────────────────────

func TestServer_FullRequestLifecycle(t *testing.T) {
	log := logger.New(logger.LevelInfo)
	evLog := eventlog.New(50)
	proxyMgr := &mockProxyManager{enabled: false}
	quitChan := make(chan struct{})

	s := &Server{
		logger: log,
		router: mux.NewRouter(),
		config: Config{
			Logger:        log,
			EventLog:      evLog,
			ProxyManager:  proxyMgr,
			ConfigPath:    "test.json",
			ListenAddress: "127.0.0.1:0",
			QuitChan:      quitChan,
		},
		lifecycleCtx: context.Background(),
	}
	s.setupRoutes()

	// Test health endpoint
	t.Run("health", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/health", nil)
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("Health check failed: %d", rec.Code)
		}
	})

	// Test status endpoint
	t.Run("status", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/status", nil)
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("Status failed: %d", rec.Code)
		}
	})

	// Test events endpoint
	t.Run("events", func(t *testing.T) {
		evLog.Add(eventlog.LevelInfo, "test", "Test event")

		req := httptest.NewRequest("GET", "/api/events", nil)
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("Events failed: %d", rec.Code)
		}
	})

	// Test proxy toggle
	t.Run("proxy_toggle", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/proxy/toggle", nil)
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("Proxy toggle failed: %d", rec.Code)
		}
	})
}

// ── Edge Cases ───────────────────────────────────────────────────────────────────

func TestRespondJSON_NilData(t *testing.T) {
	log := logger.New(logger.LevelInfo)
	s := &Server{logger: log}

	rec := httptest.NewRecorder()
	s.respondJSON(rec, http.StatusOK, nil)

	// Should write "null" to body
	if !bytes.Equal(rec.Body.Bytes(), []byte("null\n")) && !bytes.Equal(rec.Body.Bytes(), []byte("null")) {
		t.Errorf("Body = %q, want 'null'", rec.Body.String())
	}
}

func TestRespondJSON_ComplexData(t *testing.T) {
	log := logger.New(logger.LevelInfo)
	s := &Server{logger: log}

	rec := httptest.NewRecorder()
	data := map[string]interface{}{
		"string": "value",
		"int":    42,
		"nested": map[string]string{"key": "value"},
		"array":  []int{1, 2, 3},
	}

	s.respondJSON(rec, http.StatusOK, data)

	var result map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Errorf("Failed to unmarshal response: %v", err)
	}
}
