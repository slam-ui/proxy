package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"proxyclient/internal/logger"
)

func newAppProxyHandlers(t *testing.T) *AppProxyHandlers {
	t.Helper()
	s := NewServer(Config{Logger: &logger.NoOpLogger{}}, context.Background())
	return &AppProxyHandlers{server: s}
}

// --- handleCreateRule ---

func TestHandleCreateRuleRejectsUnknownFields(t *testing.T) {
	h := newAppProxyHandlers(t)
	req := httptest.NewRequest(http.MethodPost, "/api/apps/rules",
		strings.NewReader(`{"name":"test","unexpected":true}`))
	w := httptest.NewRecorder()
	h.handleCreateRule(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

func TestHandleCreateRuleRejectsOversizedBody(t *testing.T) {
	h := newAppProxyHandlers(t)
	body := `{"name":"` + strings.Repeat("a", int(maxAppProxyRequestBytes)) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/apps/rules", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.handleCreateRule(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

// --- handleUpdateRule ---

func TestHandleUpdateRuleRejectsUnknownFields(t *testing.T) {
	h := newAppProxyHandlers(t)
	req := httptest.NewRequest(http.MethodPut, "/api/apps/rules/abc",
		strings.NewReader(`{"name":"test","unexpected":true}`))
	w := httptest.NewRecorder()
	h.handleUpdateRule(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

func TestHandleUpdateRuleRejectsOversizedBody(t *testing.T) {
	h := newAppProxyHandlers(t)
	body := `{"name":"` + strings.Repeat("a", int(maxAppProxyRequestBytes)) + `"}`
	req := httptest.NewRequest(http.MethodPut, "/api/apps/rules/abc", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.handleUpdateRule(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

// --- handleLaunch ---

func TestHandleLaunchRejectsUnknownFields(t *testing.T) {
	h := newAppProxyHandlers(t)
	req := httptest.NewRequest(http.MethodPost, "/api/apps/launch",
		strings.NewReader(`{"executable":"test.exe","unexpected":true}`))
	w := httptest.NewRecorder()
	h.handleLaunch(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

func TestHandleLaunchRejectsOversizedBody(t *testing.T) {
	h := newAppProxyHandlers(t)
	body := `{"executable":"` + strings.Repeat("a", int(maxAppProxyRequestBytes)) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/apps/launch", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.handleLaunch(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

// --- handleMatchRule ---

func TestHandleMatchRuleRejectsUnknownFields(t *testing.T) {
	h := newAppProxyHandlers(t)
	req := httptest.NewRequest(http.MethodPost, "/api/apps/match",
		strings.NewReader(`{"process_path":"test.exe","unexpected":true}`))
	w := httptest.NewRecorder()
	h.handleMatchRule(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

func TestHandleMatchRuleRejectsOversizedBody(t *testing.T) {
	h := newAppProxyHandlers(t)
	body := `{"process_path":"` + strings.Repeat("a", int(maxAppProxyRequestBytes)) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/apps/match", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.handleMatchRule(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}
