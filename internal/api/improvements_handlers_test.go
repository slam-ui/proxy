package api

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"proxyclient/internal/config"
)

func TestParseImportedRulesRejectsInvalidAction(t *testing.T) {
	_, err := parseImportedRules("text", "example.com", config.RuleAction("DROP"))
	if err == nil {
		t.Fatal("expected invalid action to be rejected")
	}
	if !strings.Contains(err.Error(), "proxy | direct | block") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseImportedRulesDefaultsEmptyActionToProxy(t *testing.T) {
	rules, err := parseImportedRules("text", "example.com", "")
	if err != nil {
		t.Fatalf("parseImportedRules failed: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("len(rules) = %d, want 1", len(rules))
	}
	if rules[0].Action != config.ActionProxy {
		t.Fatalf("Action = %q, want proxy", rules[0].Action)
	}
}

func TestParseImportedRulesGFWListAcceptsRawURLBase64(t *testing.T) {
	encoded := base64.RawURLEncoding.EncodeToString([]byte("||example.com\n"))
	rules, err := parseImportedRules("gfwlist", encoded, config.ActionDirect)
	if err != nil {
		t.Fatalf("parseImportedRules gfwlist failed: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("len(rules) = %d, want 1", len(rules))
	}
	if rules[0].Value != "example.com" || rules[0].Action != config.ActionDirect {
		t.Fatalf("unexpected rule: %+v", rules[0])
	}
}

func TestBudgetPctDoesNotOverflow(t *testing.T) {
	const maxInt64 = int64(1<<63 - 1)
	got := budgetPct(maxInt64, 1)
	if got <= 0 {
		t.Fatalf("budgetPct returned non-positive value after huge input: %d", got)
	}
}

func TestBudgetExceededDoesNotOverflowLimit(t *testing.T) {
	const maxInt64 = int64(1<<63 - 1)
	if budgetExceeded(1<<30, maxInt64) {
		t.Fatal("small usage must not exceed an enormous limit")
	}
	if !budgetExceeded(maxInt64, maxInt64) {
		t.Fatal("max usage should exceed the clamped max limit")
	}
}

func TestHandleImportRulesRejectsUnknownFields(t *testing.T) {
	s, _, cleanup := buildTunServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/tun/rules/import",
		strings.NewReader(`{"format":"text","content":"example.com","unexpected":true}`))
	w := httptest.NewRecorder()
	s.handleImportRules(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

func TestHandleImportRulesRejectsOversizedBody(t *testing.T) {
	s, _, cleanup := buildTunServer(t)
	defer cleanup()

	body := `{"format":"text","content":"` + strings.Repeat("a", int(maxImportRulesRequestBytes)) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/tun/rules/import", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleImportRules(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

func TestHandleImportRulesRejectsTrailingData(t *testing.T) {
	s, _, cleanup := buildTunServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/tun/rules/import",
		strings.NewReader(`{"format":"text","content":"example.com"}{}`))
	w := httptest.NewRecorder()
	s.handleImportRules(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}
