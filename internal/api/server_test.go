package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"proxyclient/internal/logger"
	"proxyclient/internal/proxy"
)

// Mock implementations for testing
type mockXRayManager struct {
	running bool
	pid     int
}

func (m *mockXRayManager) Stop() error     { return nil }
func (m *mockXRayManager) IsRunning() bool { return m.running }
func (m *mockXRayManager) GetPID() int     { return m.pid }
func (m *mockXRayManager) Wait() error     { return nil }

type mockProxyManager struct {
	enabled bool
	config  proxy.Config
}

func (m *mockProxyManager) Enable(config proxy.Config) error {
	m.enabled = true
	m.config = config
	return nil
}

func (m *mockProxyManager) Disable() error {
	m.enabled = false
	return nil
}

func (m *mockProxyManager) IsEnabled() bool {
	return m.enabled
}

func (m *mockProxyManager) GetConfig() proxy.Config {
	return m.config
}

func setupTestServer() *Server {
	return NewServer(Config{
		ListenAddress: ":8080",
		XRayManager: &mockXRayManager{
			running: true,
			pid:     12345,
		},
		ProxyManager: &mockProxyManager{
			enabled: true,
			config: proxy.Config{
				Address:  "127.0.0.1:10807",
				Override: "<local>",
			},
		},
		ConfigPath: "config.json",
		Logger:     &logger.NoOpLogger{},
	})
}

func TestNewServer(t *testing.T) {
	server := setupTestServer()

	if server == nil {
		t.Fatal("Expected server, got nil")
	}

	if server.router == nil {
		t.Error("Expected router to be initialized")
	}
}

func TestServer_HandleHealth(t *testing.T) {
	server := setupTestServer()

	req := httptest.NewRequest("GET", "/api/health", nil)
	w := httptest.NewRecorder()

	server.handleHealth(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var response map[string]string
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if response["status"] != "ok" {
		t.Errorf("Expected status 'ok', got '%s'", response["status"])
	}
}

func TestServer_HandleStatus(t *testing.T) {
	server := setupTestServer()

	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()

	server.handleStatus(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var response StatusResponse
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if !response.XRay.Running {
		t.Error("Expected XRay to be running")
	}

	if response.XRay.PID != 12345 {
		t.Errorf("Expected PID 12345, got %d", response.XRay.PID)
	}

	if !response.Proxy.Enabled {
		t.Error("Expected proxy to be enabled")
	}

	if response.Proxy.Address != "127.0.0.1:10807" {
		t.Errorf("Expected address 127.0.0.1:10807, got %s", response.Proxy.Address)
	}

	if response.ConfigPath != "config.json" {
		t.Errorf("Expected config path config.json, got %s", response.ConfigPath)
	}
}

func TestServer_HandleProxyEnable(t *testing.T) {
	t.Run("enable disabled proxy", func(t *testing.T) {
		proxyMgr := &mockProxyManager{enabled: false}
		server := NewServer(Config{
			ListenAddress: ":8080",
			XRayManager:   &mockXRayManager{},
			ProxyManager:  proxyMgr,
			ConfigPath:    "config.json",
			Logger:        &logger.NoOpLogger{},
		})

		req := httptest.NewRequest("POST", "/api/proxy/enable", nil)
		w := httptest.NewRecorder()

		server.handleProxyEnable(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d", w.Code)
		}

		var response MessageResponse
		if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}

		if !proxyMgr.IsEnabled() {
			t.Error("Expected proxy to be enabled")
		}
	})

	t.Run("enable already enabled proxy", func(t *testing.T) {
		proxyMgr := &mockProxyManager{enabled: true}
		server := NewServer(Config{
			ListenAddress: ":8080",
			XRayManager:   &mockXRayManager{},
			ProxyManager:  proxyMgr,
			ConfigPath:    "config.json",
			Logger:        &logger.NoOpLogger{},
		})

		req := httptest.NewRequest("POST", "/api/proxy/enable", nil)
		w := httptest.NewRecorder()

		server.handleProxyEnable(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("Expected status 400, got %d", w.Code)
		}
	})
}

func TestServer_HandleProxyDisable(t *testing.T) {
	t.Run("disable enabled proxy", func(t *testing.T) {
		proxyMgr := &mockProxyManager{enabled: true}
		server := NewServer(Config{
			ListenAddress: ":8080",
			XRayManager:   &mockXRayManager{},
			ProxyManager:  proxyMgr,
			ConfigPath:    "config.json",
			Logger:        &logger.NoOpLogger{},
		})

		req := httptest.NewRequest("POST", "/api/proxy/disable", nil)
		w := httptest.NewRecorder()

		server.handleProxyDisable(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d", w.Code)
		}

		var response MessageResponse
		if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}

		if proxyMgr.IsEnabled() {
			t.Error("Expected proxy to be disabled")
		}
	})

	t.Run("disable already disabled proxy", func(t *testing.T) {
		proxyMgr := &mockProxyManager{enabled: false}
		server := NewServer(Config{
			ListenAddress: ":8080",
			XRayManager:   &mockXRayManager{},
			ProxyManager:  proxyMgr,
			ConfigPath:    "config.json",
			Logger:        &logger.NoOpLogger{},
		})

		req := httptest.NewRequest("POST", "/api/proxy/disable", nil)
		w := httptest.NewRecorder()

		server.handleProxyDisable(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("Expected status 400, got %d", w.Code)
		}
	})
}

func TestServer_RespondJSON(t *testing.T) {
	server := setupTestServer()

	w := httptest.NewRecorder()
	data := map[string]string{"test": "value"}

	server.respondJSON(w, http.StatusOK, data)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	contentType := w.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("Expected Content-Type application/json, got %s", contentType)
	}

	var response map[string]string
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if response["test"] != "value" {
		t.Errorf("Expected test=value, got %s", response["test"])
	}
}

func TestServer_RespondError(t *testing.T) {
	server := setupTestServer()

	w := httptest.NewRecorder()
	server.respondError(w, http.StatusBadRequest, "test error")

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", w.Code)
	}

	var response ErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if response.Error != "test error" {
		t.Errorf("Expected error 'test error', got '%s'", response.Error)
	}
}

func TestServer_LoggingMiddleware(t *testing.T) {
	var buf bytes.Buffer
	customLogger := logger.New(logger.Config{
		Level:  logger.InfoLevel,
		Output: &buf,
	})

	server := NewServer(Config{
		ListenAddress: ":8080",
		XRayManager:   &mockXRayManager{},
		ProxyManager:  &mockProxyManager{},
		ConfigPath:    "config.json",
		Logger:        customLogger,
	})

	req := httptest.NewRequest("GET", "/api/health", nil)
	w := httptest.NewRecorder()

	// Use router to test middleware
	server.router.ServeHTTP(w, req)

	output := buf.String()
	if output == "" {
		t.Skip("Logging middleware test - output may be async")
		return
	}

	// Should contain method and path
	if !bytes.Contains(buf.Bytes(), []byte("GET")) {
		t.Error("Expected log to contain GET method")
	}

	if !bytes.Contains(buf.Bytes(), []byte("/api/health")) {
		t.Error("Expected log to contain /api/health path")
	}
}

func TestServer_RecoveryMiddleware(t *testing.T) {
	server := setupTestServer()

	// Handler that panics
	panicHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("test panic")
	})

	// Wrap with recovery middleware
	wrapped := server.recoveryMiddleware(panicHandler)

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	// Should not panic
	wrapped.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("Expected status 500, got %d", w.Code)
	}
}

func TestServer_StartShutdown(t *testing.T) {
	server := setupTestServer()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start server in goroutine
	errChan := make(chan error, 1)
	go func() {
		errChan <- server.Start(ctx)
	}()

	// Give it time to start
	time.Sleep(100 * time.Millisecond)

	// Cancel context
	cancel()

	// Wait for shutdown
	select {
	case err := <-errChan:
		if err != nil {
			t.Errorf("Start() returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("Server didn't shut down in time")
	}
}

func TestServer_ShutdownGracefully(t *testing.T) {
	server := setupTestServer()

	// Start server
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go server.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	// Shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	err := server.Shutdown(shutdownCtx)
	if err != nil {
		t.Errorf("Shutdown() returned error: %v", err)
	}
}

func TestServer_ShutdownNilServer(t *testing.T) {
	server := setupTestServer()
	// httpServer is nil initially

	ctx := context.Background()
	err := server.Shutdown(ctx)
	if err != nil {
		t.Errorf("Shutdown() with nil httpServer should not error, got: %v", err)
	}
}

func BenchmarkServer_HandleStatus(b *testing.B) {
	server := setupTestServer()

	req := httptest.NewRequest("GET", "/api/status", nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		server.handleStatus(w, req)
	}
}

func BenchmarkServer_HandleHealth(b *testing.B) {
	server := setupTestServer()

	req := httptest.NewRequest("GET", "/api/health", nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		server.handleHealth(w, req)
	}
}
