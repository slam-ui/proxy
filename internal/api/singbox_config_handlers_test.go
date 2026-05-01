package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"proxyclient/internal/logger"
)

func TestHandleSetSingBoxConfigRejectsUnknownFields(t *testing.T) {
	s := NewServer(Config{
		ConfigPath: filepath.Join(t.TempDir(), "config.singbox.json"),
		Logger:     &logger.NoOpLogger{},
	}, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/singbox-config", strings.NewReader(`{"content":"{}","unexpected":true}`))
	w := httptest.NewRecorder()
	s.handleSetSingBoxConfig(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

func TestHandleSetSingBoxConfigRejectsOversizedBody(t *testing.T) {
	s := NewServer(Config{
		ConfigPath: filepath.Join(t.TempDir(), "config.singbox.json"),
		Logger:     &logger.NoOpLogger{},
	}, nil)

	body := `{"content":"` + strings.Repeat(" ", maxSingBoxConfigRequestBytes) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/singbox-config", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleSetSingBoxConfig(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}
