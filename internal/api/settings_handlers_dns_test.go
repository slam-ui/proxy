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

// B-7: тесты для конфигурирования DNS серверов

// TestHandleGetDNS проверяет получение DNS конфигурации
func TestHandleGetDNS(t *testing.T) {
	srv, cleanup := buildDNSTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/settings/dns", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /api/settings/dns = %d, ожидалось 200", w.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("ошибка парсинга ответа: %v", err)
	}

	// Проверяем что возвращаются значения по умолчанию
	if remoteDNS, ok := resp["remote_dns"].(string); !ok || remoteDNS == "" {
		t.Errorf("remote_dns отсутствует или пуст: %v", resp["remote_dns"])
	}
	if directDNS, ok := resp["direct_dns"].(string); !ok || directDNS == "" {
		t.Errorf("direct_dns отсутствует или пуст: %v", resp["direct_dns"])
	}
}

// TestHandleSetDNS_ValidURLs проверяет установку корректных DNS адресов
func TestHandleSetDNS_ValidURLs(t *testing.T) {
	srv, cleanup := buildDNSTestServer(t)
	defer cleanup()

	tests := []struct {
		remoteDNS string
		directDNS string
	}{
		{"https://1.1.1.1/dns-query", "udp://8.8.8.8"},
		{"tls://dns.google", "tcp://8.8.4.4"},
		{"https://cloudflare-dns.com/dns-query", "quic://dns.adguard.com"},
	}

	for _, tt := range tests {
		body := map[string]string{
			"remote_dns": tt.remoteDNS,
			"direct_dns": tt.directDNS,
		}
		data, _ := json.Marshal(body)

		req := httptest.NewRequest(http.MethodPost, "/api/settings/dns", bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("POST /api/settings/dns (%s, %s) = %d, ожидалось 200",
				tt.remoteDNS, tt.directDNS, w.Code)
		}
	}
}

// TestHandleSetDNS_InvalidRemoteDNS проверяет отклонение невалидного Remote DNS
func TestHandleSetDNS_InvalidRemoteDNS(t *testing.T) {
	srv, cleanup := buildDNSTestServer(t)
	defer cleanup()

	invalidDNS := []string{
		"http://8.8.8.8",   // неправильная схема
		"ftp://dns.google", // неправильная схема
		"8.8.8.8:53",       // без схемы
		"",                 // пуст
	}

	for _, dns := range invalidDNS {
		body := map[string]string{
			"remote_dns": dns,
			"direct_dns": "udp://8.8.8.8",
		}
		data, _ := json.Marshal(body)

		req := httptest.NewRequest(http.MethodPost, "/api/settings/dns", bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("POST /api/settings/dns (invalid=%s) = %d, ожидалось 400", dns, w.Code)
		}
	}
}

// TestHandleSetDNS_InvalidDirectDNS проверяет отклонение невалидного Direct DNS
func TestHandleSetDNS_InvalidDirectDNS(t *testing.T) {
	srv, cleanup := buildDNSTestServer(t)
	defer cleanup()

	body := map[string]string{
		"remote_dns": "https://1.1.1.1/dns-query",
		"direct_dns": "unknown://8.8.8.8",
	}
	data, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/settings/dns", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("POST /api/settings/dns (invalid direct) = %d, ожидалось 400", w.Code)
	}
}

// TestHandleSetDNS_InvalidJSON проверяет обработку неверного JSON
func TestHandleSetDNS_InvalidJSON(t *testing.T) {
	srv, cleanup := buildDNSTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/settings/dns", bytes.NewReader([]byte("invalid")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("POST /api/settings/dns (invalid JSON) = %d, ожидалось 400", w.Code)
	}
}

// buildDNSTestServer создаёт тестовый сервер с DNS endpoints
func buildDNSTestServer(t *testing.T) (*Server, func()) {
	t.Helper()
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	// Создаём data/ директорию и пустой routing.json
	if err := os.MkdirAll("data", 0755); err != nil {
		t.Fatalf("MkdirAll data/: %v", err)
	}

	srv := NewServer(Config{
		XRayManager:  &stubXray{},
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
	}, context.Background())

	SetupSettingsRoutes(srv)
	srv.FinalizeRoutes()

	return srv, func() { _ = os.Chdir(old) }
}
