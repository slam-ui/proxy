package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"proxyclient/internal/logger"
)

func TestHandleSecurityStatusDefaults(t *testing.T) {
	s := NewServer(Config{
		ListenAddress: "127.0.0.1:0",
		ProxyManager:  &stubProxy{},
		Logger:        &logger.NoOpLogger{},
	}, context.Background())

	req := httptest.NewRequest(http.MethodGet, "/api/security/status", nil)
	w := httptest.NewRecorder()
	s.handleSecurityStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/security/status = %d, body=%s", w.Code, w.Body.String())
	}
	var got securityStatusResponse
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Tunnel.Active {
		t.Fatal("Tunnel.Active = true, want false for idle test server")
	}
	if got.BackupServer.Available {
		t.Fatal("BackupServer.Available = true with no server manager")
	}
	if got.DNSGuard.Mode == "" {
		t.Fatal("DNSGuard.Mode is empty")
	}
}
