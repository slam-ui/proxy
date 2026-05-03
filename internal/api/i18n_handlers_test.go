package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"proxyclient/internal/logger"
)

func TestHandleI18nMessages(t *testing.T) {
	s := NewServer(Config{Logger: &logger.NoOpLogger{}}, context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/i18n/messages?locale=ru-RU", nil)
	w := httptest.NewRecorder()
	s.handleI18nMessages(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", w.Code, w.Body.String())
	}
	var resp struct {
		Locale   string            `json:"locale"`
		Messages map[string]string `json:"messages"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Locale != "ru" || resp.Messages["connect.button"] == "" {
		t.Fatalf("response=%+v", resp)
	}
}
