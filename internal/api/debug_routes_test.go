package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"proxyclient/internal/logger"
)

func TestAPIDebugEnabledFromEnv(t *testing.T) {
	t.Setenv("PROXY_DEBUG", "1")
	if !apiDebugEnabled() {
		t.Fatal("PROXY_DEBUG=1 should enable debug routes")
	}
	t.Setenv("PROXY_DEBUG", "")
	if apiDebugEnabled() {
		t.Fatal("empty PROXY_DEBUG should disable debug routes")
	}
}

func TestDebugRoutesOnlyWhenEnabled(t *testing.T) {
	t.Setenv("PROXY_DEBUG", "1")
	s := NewServer(Config{Logger: &logger.NoOpLogger{}}, context.Background())
	s.SetupFeatureRoutes(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("debug route status=%d, want 200", w.Code)
	}
}
