package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"proxyclient/internal/logger"
)

func newSettingsHandlers(t *testing.T) *SettingsHandlers {
	t.Helper()
	s := NewServer(Config{Logger: &logger.NoOpLogger{}}, context.Background())
	return &SettingsHandlers{server: s}
}

// --- handleSetSettings ---

func TestHandleSetSettingsRejectsUnknownFields(t *testing.T) {
	h := newSettingsHandlers(t)
	req := httptest.NewRequest(http.MethodPost, "/api/settings",
		strings.NewReader(`{"keepalive_enabled":true,"unexpected":true}`))
	w := httptest.NewRecorder()
	h.handleSetSettings(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

func TestHandleSetSettingsRejectsOversizedBody(t *testing.T) {
	h := newSettingsHandlers(t)
	body := `{"keepalive_enabled":true,"` + strings.Repeat("a", int(maxSettingsRequestBytes)) + `":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.handleSetSettings(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

// --- handleSetProxyGuard ---

func TestHandleSetProxyGuardRejectsUnknownFields(t *testing.T) {
	h := newSettingsHandlers(t)
	req := httptest.NewRequest(http.MethodPost, "/api/settings/proxy-guard",
		strings.NewReader(`{"enabled":true,"unexpected":true}`))
	w := httptest.NewRecorder()
	h.handleSetProxyGuard(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

func TestHandleSetProxyGuardRejectsOversizedBody(t *testing.T) {
	h := newSettingsHandlers(t)
	body := `{"enabled":true,"` + strings.Repeat("a", int(maxSettingsSmallRequestBytes)) + `":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/settings/proxy-guard", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.handleSetProxyGuard(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

// --- handleSetAutorun ---

func TestHandleSetAutorunRejectsUnknownFields(t *testing.T) {
	h := newSettingsHandlers(t)
	req := httptest.NewRequest(http.MethodPost, "/api/settings/autorun",
		strings.NewReader(`{"enabled":true,"unexpected":true}`))
	w := httptest.NewRecorder()
	h.handleSetAutorun(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

func TestHandleSetAutorunRejectsOversizedBody(t *testing.T) {
	h := newSettingsHandlers(t)
	body := `{"enabled":true,"` + strings.Repeat("a", int(maxSettingsSmallRequestBytes)) + `":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/settings/autorun", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.handleSetAutorun(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

// --- handleSetStartupProxy ---

func TestHandleSetStartupProxyRejectsUnknownFields(t *testing.T) {
	h := newSettingsHandlers(t)
	req := httptest.NewRequest(http.MethodPost, "/api/settings/startup-proxy",
		strings.NewReader(`{"enabled":true,"unexpected":true}`))
	w := httptest.NewRecorder()
	h.handleSetStartupProxy(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

func TestHandleSetStartupProxyRejectsOversizedBody(t *testing.T) {
	h := newSettingsHandlers(t)
	body := `{"enabled":true,"` + strings.Repeat("a", int(maxSettingsSmallRequestBytes)) + `":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/settings/startup-proxy", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.handleSetStartupProxy(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

// --- handleSetKillSwitch ---

func TestHandleSetKillSwitchRejectsUnknownFields(t *testing.T) {
	h := newSettingsHandlers(t)
	req := httptest.NewRequest(http.MethodPost, "/api/settings/killswitch",
		strings.NewReader(`{"enabled":true,"unexpected":true}`))
	w := httptest.NewRecorder()
	h.handleSetKillSwitch(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

func TestHandleSetKillSwitchRejectsOversizedBody(t *testing.T) {
	h := newSettingsHandlers(t)
	body := `{"enabled":true,"` + strings.Repeat("a", int(maxSettingsSmallRequestBytes)) + `":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/settings/killswitch", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.handleSetKillSwitch(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

// --- handleSetDNS ---

func TestHandleSetDNSRejectsUnknownFields(t *testing.T) {
	h := newSettingsHandlers(t)
	req := httptest.NewRequest(http.MethodPost, "/api/settings/dns",
		strings.NewReader(`{"remote_dns":"https://1.1.1.1/dns-query","unexpected":true}`))
	w := httptest.NewRecorder()
	h.handleSetDNS(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

func TestHandleSetDNSRejectsOversizedBody(t *testing.T) {
	h := newSettingsHandlers(t)
	body := `{"remote_dns":"https://` + strings.Repeat("a", int(maxSettingsRequestBytes)) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/settings/dns", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.handleSetDNS(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

// --- handleSetGeositeUpdate ---

func TestHandleSetGeositeUpdateRejectsUnknownFields(t *testing.T) {
	h := newSettingsHandlers(t)
	req := httptest.NewRequest(http.MethodPost, "/api/settings/geosite-update",
		strings.NewReader(`{"enabled":true,"unexpected":true}`))
	w := httptest.NewRecorder()
	h.handleSetGeositeUpdate(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

func TestHandleSetGeositeUpdateRejectsOversizedBody(t *testing.T) {
	h := newSettingsHandlers(t)
	body := `{"enabled":true,"` + strings.Repeat("a", int(maxSettingsSmallRequestBytes)) + `":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/settings/geosite-update", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.handleSetGeositeUpdate(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

func TestSettingsHandlersRejectTrailingData(t *testing.T) {
	h := newSettingsHandlers(t)
	tests := []struct {
		name string
		path string
		body string
		call func(http.ResponseWriter, *http.Request)
	}{
		{"set_settings", "/api/settings", `{"keepalive_enabled":true}{}`, h.handleSetSettings},
		{"proxy_guard", "/api/settings/proxy-guard", `{"enabled":true}{}`, h.handleSetProxyGuard},
		{"autorun", "/api/settings/autorun", `{"enabled":true}{}`, h.handleSetAutorun},
		{"startup_proxy", "/api/settings/startup-proxy", `{"enabled":true}{}`, h.handleSetStartupProxy},
		{"killswitch", "/api/settings/killswitch", `{"enabled":false}{}`, h.handleSetKillSwitch},
		{"dns", "/api/settings/dns", `{"remote_dns":"https://1.1.1.1/dns-query"}{}`, h.handleSetDNS},
		{"geosite_update", "/api/settings/geosite-update", `{"enabled":true,"interval_days":7}{}`, h.handleSetGeositeUpdate},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			w := httptest.NewRecorder()
			tt.call(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
			}
		})
	}
}
