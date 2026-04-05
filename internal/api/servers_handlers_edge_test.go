package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"proxyclient/internal/logger"
)

// ── Edge-кейсы ping (B-5) ─────────────────────────────────────────────────────

// TestPingServerWithProbes_ZeroProbes проверяет что probes=0 не вызывает панику.
func TestPingServerWithProbes_ZeroProbes(t *testing.T) {
	_, _, _, _ = pingServerWithProbes("vless://uuid@8.8.8.8:443", 0)
}

// TestPingServerWithProbes_NegativeProbes проверяет что probes<0 не вызывает панику.
func TestPingServerWithProbes_NegativeProbes(t *testing.T) {
	_, _, _, _ = pingServerWithProbes("vless://uuid@8.8.8.8:443", -1)
}

// ── Импорт из буфера обмена (B-6) ─────────────────────────────────────────────

// buildTestServer создаёт тестовый Server с зарегистрированными маршрутами.
func buildTestServer(t *testing.T) (*Server, func()) {
	t.Helper()
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	srv := NewServer(Config{
		XRayManager:  &stubXray{},
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
	}, context.Background())

	SetupServerRoutes(srv, "secret.key")
	srv.FinalizeRoutes()

	return srv, func() { _ = os.Chdir(old) }
}

// TestHandleImportClipboard_ValidURL проверяет успешный импорт валидного URL
func TestHandleImportClipboard_ValidURL(t *testing.T) {
	srv, cleanup := buildTestServer(t)
	defer cleanup()

	body := map[string]string{
		"url": "vless://12345678-1234-5678-1234-567812345678@example.com:443",
	}
	data, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/servers/import-clipboard", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("POST /api/servers/import-clipboard = %d, ожидалось 200", w.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("ошибка парсинга ответа: %v", err)
	}

	if success, ok := resp["success"].(bool); !ok || !success {
		t.Errorf("success = %v, ожидалось true", resp["success"])
	}

	server := resp["server"].(map[string]interface{})
	if name, ok := server["name"].(string); !ok || name != "Сервер example.com" {
		t.Errorf("имя сервера = %q, ожидалось 'Сервер example.com'", server["name"])
	}
}

// TestHandleImportClipboard_InvalidJSON проверяет обработку неверного JSON
func TestHandleImportClipboard_InvalidJSON(t *testing.T) {
	srv, cleanup := buildTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/servers/import-clipboard", bytes.NewReader([]byte("invalid json")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("POST /api/servers/import-clipboard (invalid JSON) = %d, ожидалось 400", w.Code)
	}
}

// TestHandleImportClipboard_EmptyURL проверяет обработку пустого URL
func TestHandleImportClipboard_EmptyURL(t *testing.T) {
	srv, cleanup := buildTestServer(t)
	defer cleanup()

	body := map[string]string{"url": ""}
	data, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/servers/import-clipboard", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("POST /api/servers/import-clipboard (empty URL) = %d, ожидалось 400", w.Code)
	}
}

// TestHandleImportClipboard_InvalidURLFormat проверяет обработку невалидного формата URL
func TestHandleImportClipboard_InvalidURLFormat(t *testing.T) {
	srv, cleanup := buildTestServer(t)
	defer cleanup()

	body := map[string]string{"url": "not-a-vless-url"}
	data, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/servers/import-clipboard", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("POST /api/servers/import-clipboard (invalid format) = %d, ожидалось 400", w.Code)
	}
}

// TestHandleImportClipboard_DuplicateURL проверяет что дублирующийся URL возвращает 409
func TestHandleImportClipboard_DuplicateURL(t *testing.T) {
	srv, cleanup := buildTestServer(t)
	defer cleanup()

	vlessURL := "vless://12345678-1234-5678-1234-567812345678@test.com:443"

	importOnce := func() int {
		body, _ := json.Marshal(map[string]string{"url": vlessURL})
		req := httptest.NewRequest(http.MethodPost, "/api/servers/import-clipboard", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)
		return w.Code
	}

	if code := importOnce(); code != http.StatusOK {
		t.Errorf("первый импорт = %d, ожидалось 200", code)
	}
	if code := importOnce(); code != http.StatusConflict {
		t.Errorf("дублирующийся импорт = %d, ожидалось 409", code)
	}
}

// TestHandleImportClipboard_FirstServerAutonActivate проверяет автоактивацию первого сервера
func TestHandleImportClipboard_FirstServerAutonActivate(t *testing.T) {
	srv, cleanup := buildTestServer(t)
	defer cleanup()

	body := map[string]string{
		"url": "vless://87654321-4321-8765-4321-876543218765@first.com:443",
	}
	data, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/servers/import-clipboard", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("первый импорт = %d, ожидалось 200", w.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("ошибка парсинга ответа: %v", err)
	}

	if success, ok := resp["success"].(bool); !ok || !success {
		t.Error("первый сервер должен быть успешно импортирован")
	}
}
