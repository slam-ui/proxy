package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"proxyclient/internal/config"
	"proxyclient/internal/hotkeys"
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

func TestHandleSetSettingsAcceptsHotkeys(t *testing.T) {
	h := newSettingsHandlers(t)
	req := httptest.NewRequest(http.MethodPost, "/api/settings",
		strings.NewReader(`{"hotkeys":{"enabled":true,"bindings":[{"action":"toggle_connection","accelerator":"alt+ctrl+p","enabled":true}]}}`))
	w := httptest.NewRecorder()
	h.handleSetSettings(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", w.Code, w.Body.String())
	}
	var resp struct {
		Hotkeys struct {
			Bindings []struct {
				Action      string `json:"action"`
				Accelerator string `json:"accelerator"`
			} `json:"bindings"`
		} `json:"hotkeys"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Hotkeys.Bindings) == 0 || resp.Hotkeys.Bindings[0].Accelerator != "Ctrl+Alt+P" {
		t.Fatalf("hotkeys response = %+v", resp.Hotkeys.Bindings)
	}
}

func TestHandleSetSettingsAcceptsCloseToTrayFalse(t *testing.T) {
	h := newSettingsHandlers(t)
	req := httptest.NewRequest(http.MethodPost, "/api/settings",
		strings.NewReader(`{"close_to_tray":false}`))
	w := httptest.NewRecorder()
	h.handleSetSettings(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", w.Code, w.Body.String())
	}
	var resp struct {
		CloseToTray bool `json:"close_to_tray"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.CloseToTray {
		t.Fatal("close_to_tray should persist false")
	}
}

func TestHandleSetSettingsAcceptsLanguage(t *testing.T) {
	h := newSettingsHandlers(t)
	req := httptest.NewRequest(http.MethodPost, "/api/settings",
		strings.NewReader(`{"language":"en"}`))
	w := httptest.NewRecorder()
	h.handleSetSettings(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", w.Code, w.Body.String())
	}
	var resp struct {
		Language string `json:"language"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Language != "en" {
		t.Fatalf("language=%q, want en", resp.Language)
	}
}

func TestHandleSetSettingsRejectsInvalidLanguage(t *testing.T) {
	h := newSettingsHandlers(t)
	req := httptest.NewRequest(http.MethodPost, "/api/settings",
		strings.NewReader(`{"language":"de"}`))
	w := httptest.NewRecorder()
	h.handleSetSettings(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

func TestHandleSetSettingsAcceptsTelemetryOptIn(t *testing.T) {
	h := newSettingsHandlers(t)
	req := httptest.NewRequest(http.MethodPost, "/api/settings",
		strings.NewReader(`{"telemetry":{"enabled":true,"crash_reports":true,"usage_events":true,"base_url":"https://telemetry.example.test/safesky"}}`))
	w := httptest.NewRecorder()
	h.handleSetSettings(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", w.Code, w.Body.String())
	}
	var resp struct {
		Telemetry config.TelemetrySettings `json:"telemetry"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Telemetry.Enabled || !resp.Telemetry.CrashReports || !resp.Telemetry.UsageEvents {
		t.Fatalf("telemetry response=%+v", resp.Telemetry)
	}
}

func TestHandleSetSettingsTelemetryOptOutDisablesSubfeatures(t *testing.T) {
	h := newSettingsHandlers(t)
	req := httptest.NewRequest(http.MethodPost, "/api/settings",
		strings.NewReader(`{"telemetry":{"enabled":false,"crash_reports":true,"usage_events":true,"base_url":"https://telemetry.example.test/safesky"}}`))
	w := httptest.NewRecorder()
	h.handleSetSettings(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", w.Code, w.Body.String())
	}
	var resp struct {
		Telemetry config.TelemetrySettings `json:"telemetry"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Telemetry.CrashReports || resp.Telemetry.UsageEvents {
		t.Fatalf("telemetry opt-out response=%+v", resp.Telemetry)
	}
}

func TestHandleSetSettingsInvokesCloseToTrayCallback(t *testing.T) {
	h := newSettingsHandlers(t)
	called := false
	h.server.config.CloseToTrayFn = func(enabled bool) {
		called = true
		if enabled {
			t.Fatal("callback enabled=true, want false")
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/settings",
		strings.NewReader(`{"close_to_tray":false}`))
	w := httptest.NewRecorder()
	h.handleSetSettings(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", w.Code, w.Body.String())
	}
	if !called {
		t.Fatal("CloseToTrayFn was not called")
	}
}

func TestHandleSetSettingsReloadsHotkeysAndReturnsConflicts(t *testing.T) {
	h := newSettingsHandlers(t)
	h.server.config.HotkeysUpdatedFn = func(settings config.HotkeySettings) []hotkeys.Conflict {
		if !settings.Enabled {
			t.Fatal("hotkeys enabled=false, want true")
		}
		return []hotkeys.Conflict{{Action: hotkeys.ActionToggleConnection, Accelerator: "Ctrl+Alt+P", Error: "already registered"}}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/settings",
		strings.NewReader(`{"hotkeys":{"enabled":true,"bindings":[{"action":"toggle_connection","accelerator":"Ctrl+Alt+P","enabled":true}]}}`))
	w := httptest.NewRecorder()
	h.handleSetSettings(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", w.Code, w.Body.String())
	}
	var resp struct {
		HotkeyConflicts []hotkeys.Conflict `json:"hotkey_conflicts"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.HotkeyConflicts) != 1 || resp.HotkeyConflicts[0].Error != "already registered" {
		t.Fatalf("hotkey_conflicts=%+v", resp.HotkeyConflicts)
	}
}

func TestHandleSetSettingsRejectsInvalidHotkey(t *testing.T) {
	h := newSettingsHandlers(t)
	req := httptest.NewRequest(http.MethodPost, "/api/settings",
		strings.NewReader(`{"hotkeys":{"enabled":true,"bindings":[{"action":"toggle_connection","accelerator":"Ctrl+Alt+F13","enabled":true}]}}`))
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
