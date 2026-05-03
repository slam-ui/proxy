package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"proxyclient/internal/logger"
)

func TestTelemetryExportOptOutReturnsEmptyEvents(t *testing.T) {
	s := NewServer(Config{Logger: &logger.NoOpLogger{}}, context.Background())
	SetupTelemetryRoutes(s)
	req := httptest.NewRequest(http.MethodGet, "/api/telemetry/export", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", w.Code, w.Body.String())
	}
	var resp struct {
		Events []json.RawMessage `json:"events"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Events) != 0 {
		t.Fatalf("events=%d, want 0", len(resp.Events))
	}
}
