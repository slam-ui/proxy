package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"proxyclient/internal/logger"
)

func TestHandleEngineDownloadRejectsUnknownFields(t *testing.T) {
	s := NewServer(Config{Logger: &logger.NoOpLogger{}}, context.Background())

	req := httptest.NewRequest(http.MethodPost, "/api/engine/download", strings.NewReader(`{"exec_path":"sing-box.exe","unexpected":true}`))
	w := httptest.NewRecorder()
	s.handleEngineDownload(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

func TestHandleEngineVersionRejectsOversizedBody(t *testing.T) {
	s := NewServer(Config{Logger: &logger.NoOpLogger{}}, context.Background())

	body := `{"exec_path":"` + strings.Repeat("a", maxEngineRequestBytes) + `"}`
	req := httptest.NewRequest(http.MethodGet, "/api/engine/version", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleEngineVersion(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}
