package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleAddRuleRejectsUnknownFields(t *testing.T) {
	_, h, cleanup := buildTunServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/tun/rules",
		strings.NewReader(`{"value":"example.com","action":"proxy","unexpected":true}`))
	w := httptest.NewRecorder()
	h.handleAddRule(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

func TestHandleAddRuleRejectsOversizedBody(t *testing.T) {
	_, h, cleanup := buildTunServer(t)
	defer cleanup()

	body := `{"value":"` + strings.Repeat("a", int(maxTunSmallRequestBytes)) + `","action":"proxy"}`
	req := httptest.NewRequest(http.MethodPost, "/api/tun/rules", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.handleAddRule(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

func TestHandleBulkReplaceRulesRejectsUnknownFields(t *testing.T) {
	_, h, cleanup := buildTunServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPut, "/api/tun/rules",
		strings.NewReader(`{"rules":[],"unexpected":true}`))
	w := httptest.NewRecorder()
	h.handleBulkReplaceRules(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

func TestHandleBulkReplaceRulesRejectsOversizedBody(t *testing.T) {
	_, h, cleanup := buildTunServer(t)
	defer cleanup()

	body := `{"rules":[{"value":"` + strings.Repeat("a", int(maxTunRulesRequestBytes)) + `","action":"proxy"}]}`
	req := httptest.NewRequest(http.MethodPut, "/api/tun/rules", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.handleBulkReplaceRules(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

func TestHandleSetDefaultRejectsUnknownFields(t *testing.T) {
	_, h, cleanup := buildTunServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/tun/default",
		strings.NewReader(`{"action":"direct","unexpected":true}`))
	w := httptest.NewRecorder()
	h.handleSetDefault(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

func TestHandleSetDefaultRejectsOversizedBody(t *testing.T) {
	_, h, cleanup := buildTunServer(t)
	defer cleanup()

	body := `{"action":"` + strings.Repeat("a", int(maxTunSmallRequestBytes)) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/tun/default", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.handleSetDefault(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

func TestHandleImportRejectsUnknownFields(t *testing.T) {
	_, h, cleanup := buildTunServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/tun/import",
		strings.NewReader(`{"default_action":"proxy","rules":[],"unexpected":true}`))
	w := httptest.NewRecorder()
	h.handleImport(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

func TestHandleImportRejectsOversizedBody(t *testing.T) {
	_, h, cleanup := buildTunServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/tun/import",
		strings.NewReader(strings.Repeat(" ", int(maxTunImportBytes)+1)))
	w := httptest.NewRecorder()
	h.handleImport(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

func TestTunHandlersRejectTrailingData(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
		call func(*TunHandlers, http.ResponseWriter, *http.Request)
	}{
		{"add_rule", "/api/tun/rules", `{"value":"example.com","action":"proxy"}{}`, func(h *TunHandlers, w http.ResponseWriter, r *http.Request) { h.handleAddRule(w, r) }},
		{"bulk_replace", "/api/tun/rules", `{"rules":[]}{}`, func(h *TunHandlers, w http.ResponseWriter, r *http.Request) { h.handleBulkReplaceRules(w, r) }},
		{"set_default", "/api/tun/default", `{"action":"direct"}{}`, func(h *TunHandlers, w http.ResponseWriter, r *http.Request) { h.handleSetDefault(w, r) }},
		{"import", "/api/tun/import", `{"default_action":"proxy","rules":[]}{}`, func(h *TunHandlers, w http.ResponseWriter, r *http.Request) { h.handleImport(w, r) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, h, cleanup := buildTunServer(t)
			defer cleanup()

			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			w := httptest.NewRecorder()
			tt.call(h, w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
			}
		})
	}
}
