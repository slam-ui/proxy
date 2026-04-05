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
	"testing"
	"time"

	"proxyclient/internal/eventlog"
	"proxyclient/internal/logger"

	"github.com/gorilla/mux"
)

// ── /api/status ───────────────────────────────────────────────────────────────

func TestHandleStatus_ReturnsJSON(t *testing.T) {
	s := &Server{
		logger: logger.New(logger.LevelInfo),
		router: mux.NewRouter(),
		config: Config{
			Logger:       logger.New(logger.LevelInfo),
			EventLog:     eventlog.New(50),
			ProxyManager: &mockProxyManager{address: "127.0.0.1:8080"},
			ConfigPath:   "test.json",
		},
	}
	s.setupRoutes()

	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest("GET", "/api/status", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}
	var resp StatusResponse
	if err := jsonDecode(rec, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ConfigPath != "test.json" {
		t.Errorf("ConfigPath = %q, want 'test.json'", resp.ConfigPath)
	}
}

// ── /api/events ───────────────────────────────────────────────────────────────

func TestHandleEvents_ReturnsEmpty_WhenNoEventLog(t *testing.T) {
	s := newHandlerServer(t, nil)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest("GET", "/api/events", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}
	var resp map[string]interface{}
	if err := jsonDecode(rec, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["events"] == nil {
		t.Error("events field should exist")
	}
}

func TestHandleEvents_ReturnsEvents(t *testing.T) {
	evLog := eventlog.New(50)
	evLog.Add(eventlog.LevelInfo, "test", "Test message")

	s := newHandlerServer(t, evLog)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest("GET", "/api/events", nil))

	var resp map[string]interface{}
	if err := jsonDecode(rec, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if events, ok := resp["events"].([]interface{}); !ok || len(events) != 1 {
		t.Errorf("expected 1 event, got %v", resp["events"])
	}
}

func TestHandleEvents_SinceParameter(t *testing.T) {
	evLog := eventlog.New(50)
	evLog.Add(eventlog.LevelInfo, "test", "First")
	id := evLog.GetLatestID()
	evLog.Add(eventlog.LevelInfo, "test", "Second")

	s := newHandlerServer(t, evLog)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest("GET", fmt.Sprintf("/api/events?since=%d", id), nil))

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}
}

// ── /api/events/clear ─────────────────────────────────────────────────────────

func TestHandleEventsClear_ClearsLog(t *testing.T) {
	evLog := eventlog.New(50)
	evLog.Add(eventlog.LevelInfo, "test", "Test")

	s := newHandlerServer(t, evLog)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest("POST", "/api/events/clear", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}
	if evLog.GetLatestID() != 0 {
		t.Error("Event log should be empty after clear")
	}
}

func TestHandleEventsClear_NoPanic_WhenNilEventLog(t *testing.T) {
	s := newHandlerServer(t, nil)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest("POST", "/api/events/clear", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}
}

// ── /api/health ───────────────────────────────────────────────────────────────

func TestHandleHealth_ReturnsOK(t *testing.T) {
	s := newHandlerServer(t, nil)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest("GET", "/api/health", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}
	var resp map[string]string
	if err := jsonDecode(rec, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["status"] != "ok" {
		t.Errorf("status = %q, want 'ok'", resp["status"])
	}
}

// ── /api/quit ─────────────────────────────────────────────────────────────────

func TestHandleQuit_ClosesQuitChan(t *testing.T) {
	quitChan := make(chan struct{})
	s := newHandlerServerWithQuit(t, nil, quitChan)
	s.router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/quit", nil))

	select {
	case <-quitChan:
	case <-time.After(time.Second):
		t.Error("Quit channel was not closed")
	}
}

func TestHandleQuit_NoPanic_WhenNilQuitChan(t *testing.T) {
	s := newHandlerServer(t, nil)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest("POST", "/api/quit", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestHandleQuit_OnlyOnce(t *testing.T) {
	quitChan := make(chan struct{})
	s := newHandlerServerWithQuit(t, nil, quitChan)
	for i := 0; i < 2; i++ {
		s.router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/quit", nil))
	}
	select {
	case <-quitChan:
	case <-time.After(time.Second):
		t.Error("Quit channel was not closed")
	}
}

// ── /api/proxy/enable|disable ─────────────────────────────────────────────────

func TestHandleProxyEnable_Returns400_WhenAlreadyEnabled(t *testing.T) {
	s := newHandlerServerWithProxy(t, &mockProxyManager{enabled: true})
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest("POST", "/api/proxy/enable", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleProxyDisable_Returns400_WhenAlreadyDisabled(t *testing.T) {
	s := newHandlerServerWithProxy(t, &mockProxyManager{enabled: false})
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest("POST", "/api/proxy/disable", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// ── respondJSON / respondError ────────────────────────────────────────────────

func TestResponseWriter_CapturesStatusCode(t *testing.T) {
	rw := &responseWriter{ResponseWriter: httptest.NewRecorder(), statusCode: http.StatusOK}
	rw.WriteHeader(http.StatusCreated)
	if rw.statusCode != http.StatusCreated {
		t.Errorf("statusCode = %d, want %d", rw.statusCode, http.StatusCreated)
	}
}

func TestRespondJSON_SetsContentType(t *testing.T) {
	s := &Server{logger: logger.New(logger.LevelInfo)}
	rec := httptest.NewRecorder()
	s.respondJSON(rec, http.StatusOK, map[string]string{"test": "value"})
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want 'application/json'", ct)
	}
}

func TestRespondJSON_NilData(t *testing.T) {
	s := &Server{logger: logger.New(logger.LevelInfo)}
	rec := httptest.NewRecorder()
	s.respondJSON(rec, http.StatusOK, nil)
	if !bytes.Equal(rec.Body.Bytes(), []byte("null\n")) && !bytes.Equal(rec.Body.Bytes(), []byte("null")) {
		t.Errorf("Body = %q, want 'null'", rec.Body.String())
	}
}

func TestRespondJSON_ComplexData(t *testing.T) {
	s := &Server{logger: logger.New(logger.LevelInfo)}
	rec := httptest.NewRecorder()
	s.respondJSON(rec, http.StatusOK, map[string]interface{}{
		"string": "value", "int": 42,
		"nested": map[string]string{"key": "value"}, "array": []int{1, 2, 3},
	})
	var result map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Errorf("unmarshal: %v", err)
	}
}

func TestRespondError_ReturnsCorrectFormat(t *testing.T) {
	s := &Server{logger: logger.New(logger.LevelInfo)}
	rec := httptest.NewRecorder()
	s.respondError(rec, http.StatusBadRequest, "test error")
	var resp ErrorResponse
	if err := jsonDecode(rec, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error != "test error" {
		t.Errorf("Error = %q, want 'test error'", resp.Error)
	}
}

func TestAddSilentPath_InvalidatesCache(t *testing.T) {
	s := &Server{
		logger:      logger.New(logger.LevelInfo),
		silentCache: map[string]bool{"/api/status": true},
	}
	s.addSilentPath("/api/new")
	if s.silentCache != nil {
		t.Error("silentCache should be nil after addSilentPath")
	}
}

// ── switchClashMode ───────────────────────────────────────────────────────────

func TestSwitchClashMode_DoesNotPanic_OnError(t *testing.T) {
	log := logger.New(logger.LevelInfo)
	switchClashMode(context.Background(), log, "rule")
	switchClashMode(context.Background(), log, "direct")
}

func TestSwitchClashMode_HandlesNilLogger(t *testing.T) {
	switchClashMode(context.Background(), nil, "rule")
}

// ── Fuzz ──────────────────────────────────────────────────────────────────────

func FuzzHandleEventsSince(f *testing.F) {
	for _, seed := range []string{"", "0", "1", "abc", "-1", "999999"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, since string) {
		evLog := eventlog.New(50)
		s := newHandlerServer(t, evLog)
		url := "/api/events"
		if since != "" {
			url += "?since=" + since
		}
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
		}
	})
}

// ── Benchmarks ────────────────────────────────────────────────────────────────

func BenchmarkHandleStatus(b *testing.B) {
	s := &Server{
		logger: logger.New(logger.LevelInfo),
		router: mux.NewRouter(),
		config: Config{
			Logger:       logger.New(logger.LevelInfo),
			EventLog:     eventlog.New(50),
			ProxyManager: &mockProxyManager{},
		},
	}
	s.setupRoutes()
	req := httptest.NewRequest("GET", "/api/status", nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.router.ServeHTTP(httptest.NewRecorder(), req)
	}
}

// ── Integration ───────────────────────────────────────────────────────────────

func TestServer_FullRequestLifecycle(t *testing.T) {
	evLog := eventlog.New(50)
	proxyMgr := &mockProxyManager{}
	quitChan := make(chan struct{})

	s := &Server{
		logger: logger.New(logger.LevelInfo),
		router: mux.NewRouter(),
		config: Config{
			Logger:        logger.New(logger.LevelInfo),
			EventLog:      evLog,
			ProxyManager:  proxyMgr,
			ConfigPath:    "test.json",
			ListenAddress: "127.0.0.1:0",
			QuitChan:      quitChan,
		},
		lifecycleCtx: context.Background(),
	}
	s.setupRoutes()

	for _, tc := range []struct {
		name, method, path string
	}{
		{"health", "GET", "/api/health"},
		{"status", "GET", "/api/status"},
		{"proxy_toggle", "POST", "/api/proxy/toggle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			s.router.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
			if rec.Code != http.StatusOK {
				t.Errorf("%s: status=%d", tc.name, rec.Code)
			}
		})
	}

	t.Run("events", func(t *testing.T) {
		evLog.Add(eventlog.LevelInfo, "test", "Test event")
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, httptest.NewRequest("GET", "/api/events", nil))
		if rec.Code != http.StatusOK {
			t.Errorf("events: status=%d", rec.Code)
		}
	})
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// newHandlerServer создаёт Server со стандартными маршрутами и опциональным eventLog.
func newHandlerServer(t *testing.T, evLog *eventlog.Log) *Server {
	t.Helper()
	s := &Server{
		logger: logger.New(logger.LevelInfo),
		router: mux.NewRouter(),
		config: Config{
			Logger:   logger.New(logger.LevelInfo),
			EventLog: evLog,
		},
	}
	s.setupRoutes()
	return s
}

func newHandlerServerWithProxy(t *testing.T, pm *mockProxyManager) *Server {
	t.Helper()
	s := &Server{
		logger: logger.New(logger.LevelInfo),
		router: mux.NewRouter(),
		config: Config{
			Logger:       logger.New(logger.LevelInfo),
			ProxyManager: pm,
		},
	}
	s.setupRoutes()
	return s
}

func newHandlerServerWithQuit(t *testing.T, evLog *eventlog.Log, quitChan chan struct{}) *Server {
	t.Helper()
	s := &Server{
		logger: logger.New(logger.LevelInfo),
		router: mux.NewRouter(),
		config: Config{
			Logger:   logger.New(logger.LevelInfo),
			EventLog: evLog,
			QuitChan: quitChan,
		},
	}
	s.setupRoutes()
	return s
}

// jsonDecode декодирует тело ответа в v.
func jsonDecode(rec *httptest.ResponseRecorder, v interface{}) error {
	return json.NewDecoder(rec.Body).Decode(v)
}
