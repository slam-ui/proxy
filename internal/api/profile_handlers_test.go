package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"proxyclient/internal/apprules"
	"proxyclient/internal/config"
	"proxyclient/internal/logger"
)

func newProfileTestHandler() *ProfileHandlers {
	s := NewServer(Config{Logger: &logger.NoOpLogger{}}, context.Background())
	return &ProfileHandlers{server: s, metaCache: make(map[string]profileMeta)}
}

func TestHandleSaveProfileRejectsUnknownFields(t *testing.T) {
	h := newProfileTestHandler()

	req := httptest.NewRequest(http.MethodPost, "/api/profiles", strings.NewReader(`{"name":"Work","routing":{"rules":[]},"unexpected":true}`))
	w := httptest.NewRecorder()
	h.handleSave(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

func TestHandleSaveProfileRejectsOversizedBody(t *testing.T) {
	h := newProfileTestHandler()

	body := `{"name":"Work","routing":{"rules":[]},"padding":"` + strings.Repeat("a", maxProfileSaveRequestBytes) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/profiles", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.handleSave(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

func TestProfilePathRejectsWindowsDeviceNames(t *testing.T) {
	for _, name := range []string{"CON.json", "PRN.json", "AUX.json", "NUL.json", "COM1.json", "LPT9.json"} {
		if path, err := profilePath(name); err == nil {
			t.Fatalf("profilePath(%q) = %q, want error", name, path)
		}
	}
}

func TestProfilesSaveFullModelRoundTrip(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(old) }()

	srv := NewServer(Config{Logger: &logger.NoOpLogger{}}, context.Background())
	SetupProfileRoutes(srv)

	payload := map[string]interface{}{
		"id":          "work",
		"name":        "Work",
		"description": "Corporate profile",
		"icon":        "briefcase",
		"color":       "#3366ff",
		"server_selector": map[string]string{
			"mode":      "specific",
			"server_id": "srv1",
		},
		"app_rules": []apprules.Rule{{
			ID:      "app1",
			Name:    "Browser",
			Pattern: "chrome.exe",
			Action:  apprules.ActionProxy,
			Enabled: true,
		}},
		"routing_rules": []config.RoutingRule{{
			Value:  "Example.COM",
			Type:   config.RuleTypeDomain,
			Action: config.ActionProxy,
		}},
		"dns_config": map[string]interface{}{
			"remote_dns": "https://1.1.1.1/dns-query",
			"direct_dns": "udp://8.8.8.8",
		},
		"split_tunnel": []string{"10.0.0.0/8"},
		"auto_connect": true,
		"hotkey":       "Ctrl+Alt+W",
	}
	w := postJSON(t, srv.router, "/api/profiles", payload)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /api/profiles = %d, body=%s", w.Code, w.Body.String())
	}

	w = getJSON(t, srv.router, "/api/profiles/Work")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/profiles/Work = %d, body=%s", w.Code, w.Body.String())
	}
	var p Profile
	if err := json.NewDecoder(w.Body).Decode(&p); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if p.ID != "work" || p.Description != "Corporate profile" || p.ServerSelector.ServerID != "srv1" || !p.AutoConnect {
		t.Fatalf("profile metadata not preserved: %+v", p)
	}
	if len(p.AppRules) != 1 || p.AppRules[0].Pattern != "chrome.exe" {
		t.Fatalf("app rules not preserved: %+v", p.AppRules)
	}
	if len(p.RoutingRules) != 1 || p.RoutingRules[0].Value != "example.com" {
		t.Fatalf("routing rules not normalized/preserved: %+v", p.RoutingRules)
	}
	if p.DNSConfig == nil || p.DNSConfig.DirectDNS != "udp://8.8.8.8" {
		t.Fatalf("dns config not preserved: dns=%+v", p.DNSConfig)
	}
}

func TestProfilesSaveRejectsSpecificSelectorWithoutServer(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(old) }()

	srv := NewServer(Config{Logger: &logger.NoOpLogger{}}, context.Background())
	SetupProfileRoutes(srv)
	w := postJSON(t, srv.router, "/api/profiles", map[string]interface{}{
		"name":            "Broken",
		"server_selector": map[string]string{"mode": "specific"},
		"routing":         config.RoutingConfig{DefaultAction: config.ActionProxy},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/profiles specific without server = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestProfilesImportExportRoundTrip(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(old) }()

	srv := NewServer(Config{Logger: &logger.NoOpLogger{}}, context.Background())
	SetupProfileRoutes(srv)
	profile := Profile{
		ID:             "streaming",
		Name:           "Streaming",
		Description:    "Media",
		ServerSelector: ServerSelector{Mode: "auto"},
		Routing:        config.RoutingConfig{DefaultAction: config.ActionProxy, Rules: []config.RoutingRule{{Value: "youtube.com", Type: config.RuleTypeDomain, Action: config.ActionProxy}}},
		RoutingRules:   []config.RoutingRule{{Value: "youtube.com", Type: config.RuleTypeDomain, Action: config.ActionProxy}},
		AutoConnect:    true,
	}
	body, _ := json.Marshal(profile)
	req := httptest.NewRequest(http.MethodPost, "/api/profiles/import", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /api/profiles/import = %d, body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/profiles/Streaming/export", nil)
	w = httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/profiles/Streaming/export = %d, body=%s", w.Code, w.Body.String())
	}
	var exported Profile
	if err := json.NewDecoder(w.Body).Decode(&exported); err != nil {
		t.Fatalf("Decode exported: %v", err)
	}
	if exported.ID != "streaming" || exported.Description != "Media" || !exported.AutoConnect {
		t.Fatalf("unexpected exported profile: %+v", exported)
	}
}

func TestEnsureDefaultProfilesCreatesPresetsOnlyWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(old) }()
	if err := os.MkdirAll(profilesDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := ensureDefaultProfiles(); err != nil {
		t.Fatalf("ensureDefaultProfiles: %v", err)
	}
	entries, err := os.ReadDir(profilesDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("preset count=%d, want 4", len(entries))
	}
	if err := ensureDefaultProfiles(); err != nil {
		t.Fatalf("second ensureDefaultProfiles: %v", err)
	}
	entries2, err := os.ReadDir(profilesDir)
	if err != nil {
		t.Fatalf("ReadDir2: %v", err)
	}
	if len(entries2) != 4 {
		t.Fatalf("second preset count=%d, want 4", len(entries2))
	}
}
