package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"proxyclient/internal/logger"
)

// --- handleConnectionRule ---

func TestHandleConnectionRuleRejectsUnknownFields(t *testing.T) {
	s := NewServer(Config{Logger: &logger.NoOpLogger{}}, context.Background())
	req := httptest.NewRequest(http.MethodPost, "/api/connections/rule",
		strings.NewReader(`{"value":"example.com","unexpected":true}`))
	w := httptest.NewRecorder()
	s.handleConnectionRule(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

func TestHandleConnectionRuleRejectsOversizedBody(t *testing.T) {
	s := NewServer(Config{Logger: &logger.NoOpLogger{}}, context.Background())
	body := `{"value":"` + strings.Repeat("a", int(maxClientFeaturesRequestBytes)) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/connections/rule", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleConnectionRule(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

// --- handleDNSGuardSet ---

func TestHandleDNSGuardSetRejectsUnknownFields(t *testing.T) {
	s := NewServer(Config{Logger: &logger.NoOpLogger{}}, context.Background())
	req := httptest.NewRequest(http.MethodPost, "/api/security/dns-guard",
		strings.NewReader(`{"enabled":true,"unexpected":true}`))
	w := httptest.NewRecorder()
	s.handleDNSGuardSet(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

func TestHandleDNSGuardSetRejectsOversizedBody(t *testing.T) {
	s := NewServer(Config{Logger: &logger.NoOpLogger{}}, context.Background())
	body := `{"enabled":true,"mode":"` + strings.Repeat("a", int(maxClientFeaturesRequestBytes)) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/security/dns-guard", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleDNSGuardSet(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

// --- handleTrafficBudgetSet ---

func TestHandleTrafficBudgetSetRejectsUnknownFields(t *testing.T) {
	s := NewServer(Config{Logger: &logger.NoOpLogger{}}, context.Background())
	req := httptest.NewRequest(http.MethodPost, "/api/security/traffic-budget",
		strings.NewReader(`{"enabled":true,"unexpected":true}`))
	w := httptest.NewRecorder()
	s.handleTrafficBudgetSet(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

func TestHandleTrafficBudgetSetRejectsOversizedBody(t *testing.T) {
	s := NewServer(Config{Logger: &logger.NoOpLogger{}}, context.Background())
	body := `{"enabled":true,"session_limit_mb":` + strings.Repeat("1", int(maxClientFeaturesRequestBytes)) + `}`
	req := httptest.NewRequest(http.MethodPost, "/api/security/traffic-budget", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleTrafficBudgetSet(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}
