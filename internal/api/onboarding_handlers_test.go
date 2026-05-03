package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"proxyclient/internal/config"
	"proxyclient/internal/logger"
)

func newOnboardingHandlers(t *testing.T) *OnboardingHandlers {
	t.Helper()
	s := NewServer(Config{Logger: &logger.NoOpLogger{}}, context.Background())
	dir := t.TempDir()
	return &OnboardingHandlers{
		server:       s,
		markerPath:   filepath.Join(dir, "onboarded"),
		settingsPath: filepath.Join(dir, "settings.json"),
	}
}

func TestOnboardingStatusMissing(t *testing.T) {
	h := newOnboardingHandlers(t)
	req := httptest.NewRequest(http.MethodGet, "/api/onboarding/status", nil)
	w := httptest.NewRecorder()
	h.handleStatus(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", w.Code, w.Body.String())
	}
	var resp struct {
		Onboarded bool `json:"onboarded"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Onboarded {
		t.Fatal("onboarded=true, want false")
	}
}

func TestOnboardingCompleteCreatesMarker(t *testing.T) {
	h := newOnboardingHandlers(t)
	req := httptest.NewRequest(http.MethodPost, "/api/onboarding/complete", nil)
	w := httptest.NewRecorder()
	h.handleComplete(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", w.Code, w.Body.String())
	}
	var resp struct {
		Onboarded bool `json:"onboarded"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Onboarded {
		t.Fatal("onboarded=false, want true")
	}
}

func TestOnboardingCompleteAppliesSmartDefaults(t *testing.T) {
	h := newOnboardingHandlers(t)
	settings := config.DefaultAppSettings()
	settings.StartProxyOnLaunch = true
	settings.Updates.Enabled = false
	settings.LeakTest.Enabled = false
	if err := config.SaveAppSettings(h.settingsPath, settings); err != nil {
		t.Fatalf("SaveAppSettings: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/onboarding/complete", nil)
	w := httptest.NewRecorder()
	h.handleComplete(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", w.Code, w.Body.String())
	}
	got, err := config.LoadAppSettings(h.settingsPath)
	if err != nil {
		t.Fatalf("LoadAppSettings: %v", err)
	}
	if got.StartProxyOnLaunch || !got.Updates.Enabled || !got.LeakTest.Enabled {
		t.Fatalf("settings after onboarding = %+v", got)
	}
}
