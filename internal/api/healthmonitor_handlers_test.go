package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"proxyclient/internal/config"
	"proxyclient/internal/healthmonitor"
)

func TestHandleHealthReturnsSnapshots(t *testing.T) {
	srv, _, cleanup := buildServersServer(t)
	defer cleanup()
	h := &ServersHandlers{server: srv}
	h.health = healthmonitor.New(healthmonitor.Options{})
	h.health.Record("srv1", 42*time.Millisecond, true)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/servers/health", nil)
	h.handleHealth(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("handleHealth = %d, body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Servers []healthmonitor.Snapshot `json:"servers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(body.Servers) != 1 || body.Servers[0].ID != "srv1" || body.Servers[0].Status != "ok" {
		t.Fatalf("unexpected health response: %+v", body.Servers)
	}
}

func TestShouldFailoverUsesHealthScoreAndCooldown(t *testing.T) {
	srv, secretKeyPath, cleanup := buildServersServer(t)
	defer cleanup()
	h := &ServersHandlers{server: srv, secretKey: secretKeyPath}
	activeURL := "vless://00000000-0000-0000-0000-000000000000@slow.example.com:443?encryption=none"
	bestURL := "vless://00000000-0000-0000-0000-000000000001@fast.example.com:443?encryption=none"
	if err := saveServers([]ServerEntry{
		{ID: "slow", Name: "Slow", URL: activeURL, CountryCode: "??"},
		{ID: "fast", Name: "Fast", URL: bestURL, CountryCode: "??"},
	}); err != nil {
		t.Fatalf("saveServers: %v", err)
	}
	if err := config.WriteSecretKey(secretKeyPath, activeURL); err != nil {
		t.Fatalf("WriteSecretKey: %v", err)
	}
	h.health = healthmonitor.New(healthmonitor.Options{})
	for i := 0; i < 5; i++ {
		h.health.Record("slow", 700*time.Millisecond, true)
		h.health.Record("fast", 40*time.Millisecond, true)
	}
	settings := config.SmartFailoverSettings{Enabled: true, MaxLatencyMs: 500, CheckIntervalSec: 60}
	if !h.shouldFailover(context.Background(), settings, time.Time{}) {
		t.Fatal("shouldFailover returned false for degraded active server")
	}
	if h.shouldFailover(context.Background(), settings, time.Now()) {
		t.Fatal("shouldFailover ignored cooldown")
	}
}
