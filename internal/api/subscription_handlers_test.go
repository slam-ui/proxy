package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"proxyclient/internal/config"
	"proxyclient/internal/logger"
	"proxyclient/internal/subscription"
)

func TestApplySubscriptionServersKeepsManualAndMarksDeleted(t *testing.T) {
	srv, _, cleanup := buildServersServer(t)
	defer cleanup()
	h := &ServersHandlers{server: srv, secretKey: "secret.key"}

	manual := ServerEntry{
		ID:          "manual",
		Name:        "Manual",
		URL:         "vless://00000000-0000-0000-0000-000000000000@manual.example.com:443?encryption=none",
		CountryCode: "??",
		AddedAt:     time.Now().Unix(),
	}
	if err := saveServers([]ServerEntry{
		manual,
		{
			ID:              "old-sub",
			Name:            "Old",
			URL:             "vless://00000000-0000-0000-0000-000000000001@old.example.com:443?encryption=none",
			CountryCode:     "??",
			AddedAt:         time.Now().Unix(),
			SubscriptionID:  "sub1",
			SubscriptionKey: "vless://00000000-0000-0000-0000-000000000001@old.example.com:443",
		},
	}); err != nil {
		t.Fatalf("saveServers: %v", err)
	}

	result := subscription.UpdateResult{Servers: []subscription.ServerEntry{{
		Name: "New",
		URI:  "vless://00000000-0000-0000-0000-000000000002@new.example.com:443?encryption=none",
	}}}
	if err := h.applySubscriptionServers(context.Background(), subscription.Subscription{ID: "sub1"}, result); err != nil {
		t.Fatalf("applySubscriptionServers: %v", err)
	}
	list, err := loadServers()
	if err != nil {
		t.Fatalf("loadServers: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("servers=%d, want 3: %+v", len(list), list)
	}
	var manualFound, deletedOld, newFound bool
	for _, server := range list {
		switch server.ID {
		case "manual":
			manualFound = !server.Deleted && server.SubscriptionID == ""
		case "old-sub":
			deletedOld = server.Deleted
		default:
			if server.SubscriptionID == "sub1" && !server.Deleted && server.Name == "New" {
				newFound = true
			}
		}
	}
	if !manualFound || !deletedOld || !newFound {
		t.Fatalf("unexpected applied list: %+v", list)
	}
}

func TestSubscriptionHandlersAddUpdatesServers(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/"+config.DataDir, 0755); err != nil {
		t.Fatalf("MkdirAll data/: %v", err)
	}
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(old) }()
	secretKeyPath := dir + "/secret.key"
	srv := NewServer(Config{
		ListenAddress: ":0",
		XRayManager:   &stubXray{running: false},
		ProxyManager:  &stubProxy{},
		Logger:        &logger.NoOpLogger{},
	}, context.Background())
	h := &ServersHandlers{server: srv, secretKey: secretKeyPath}
	srv.serversHandlers = h

	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("subscription-userinfo", "upload=1; download=2; total=10")
		_, _ = w.Write([]byte("vless://00000000-0000-0000-0000-000000000000@example.com:443?encryption=none#sub"))
	}))
	defer ts.Close()
	client := ts.Client()
	client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // nosec G402: local httptest TLS certificate.
	mgr, err := subscription.NewManager(subscription.Options{
		Dir:          t.TempDir(),
		Client:       client,
		IsSupported:  isSupportedServerURI,
		ApplyServers: h.applySubscriptionServers,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	srv.subscriptions = mgr
	SetupSubscriptionRoutes(srv)

	body, _ := json.Marshal(map[string]string{
		"name":         "Sub",
		"url":          ts.URL,
		"update_every": "1h",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/subscriptions", bytes.NewReader(body))
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /api/subscriptions = %d, body=%s", w.Code, w.Body.String())
	}
	list, err := loadServers()
	if err != nil {
		t.Fatalf("loadServers: %v", err)
	}
	if len(list) != 1 || list[0].SubscriptionID == "" {
		t.Fatalf("subscription server not applied: %+v", list)
	}
	if raw, err := os.ReadFile(secretKeyPath); err != nil || len(raw) == 0 {
		t.Fatalf("secret key was not auto-activated: len=%d err=%v", len(raw), err)
	}
}
