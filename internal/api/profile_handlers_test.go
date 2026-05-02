package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
