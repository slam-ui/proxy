package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"proxyclient/internal/config"
	"proxyclient/internal/logger"
)

// buildServersServer создаёт Server + ServersHandlers с изолированным tmpDir.
// tunHandlers намеренно НЕ инициализируется — handleConnect вернёт restart_required=true.
// Возвращает (srv, secretKeyPath, cleanup).
func buildServersServer(t *testing.T) (*Server, string, func()) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/"+config.DataDir, 0755); err != nil {
		t.Fatalf("MkdirAll data/: %v", err)
	}
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	secretKeyPath := dir + "/secret.key"
	srv := NewServer(Config{
		ListenAddress: ":0",
		XRayManager:   &stubXray{running: false},
		ProxyManager:  &stubProxy{},
		Logger:        &logger.NoOpLogger{},
	}, context.Background())

	// Регистрируем серверные маршруты с реальным secretKeyPath.
	SetupServerRoutes(srv, secretKeyPath)
	srv.FinalizeRoutes()

	return srv, secretKeyPath, func() { os.Chdir(old) }
}

// addServer добавляет сервер через POST /api/servers и возвращает его ID.
func addServer(t *testing.T, srv *Server, name, url, cc string) string {
	t.Helper()
	w := postJSON(t, srv.router, "/api/servers", map[string]string{
		"name": name, "url": url, "country_code": cc,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("POST /api/servers %q = %d, body=%s", name, w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	srv2, ok := resp["server"].(map[string]interface{})
	if !ok {
		t.Fatalf("server field missing in response: %s", w.Body.String())
	}
	id, _ := srv2["id"].(string)
	if id == "" {
		t.Fatalf("пустой id в ответе: %s", w.Body.String())
	}
	return id
}

// ── Тест 1: handleConnect возвращает restart_required=true без TUN ──────────
//
// Когда tunHandlers не инициализирован (типичный сценарий unit-теста и
// упрощённого запуска без TUN), handleConnect должен вернуть restart_required=true
// чтобы фронтенд показал "перезапустите прокси".
func TestHandleConnect_NoTun_ReturnsRestartRequired(t *testing.T) {
	srv, _, cleanup := buildServersServer(t)
	defer cleanup()

	const urlA = "vless://uuid-aaa@server-a.test:443?sni=a.test&pbk=k-a&sid=s-a"
	id := addServer(t, srv, "Сервер A", urlA, "DE")

	req := httptest.NewRequest(http.MethodPost, "/api/servers/"+id+"/connect", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	t.Logf("POST /connect → %d %s", w.Code, w.Body.String())

	if w.Code != http.StatusOK {
		t.Fatalf("ожидался 200, получили %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("json decode: %v", err)
	}

	restartRequired, _ := resp["restart_required"].(bool)
	if !restartRequired {
		t.Errorf("без TUN: restart_required должен быть true (нет автоматического apply), получили %v", resp["restart_required"])
	}

	success, _ := resp["success"].(bool)
	if !success {
		t.Errorf("success должен быть true, получили %v", resp["success"])
	}
}

// ── Тест 2: handleConnect записывает URL в secret.key ────────────────────────
//
// После POST /connect secret.key должен читаться как URL сервера.
func TestHandleConnect_WritesSecretKey(t *testing.T) {
	srv, secretKeyPath, cleanup := buildServersServer(t)
	defer cleanup()

	const urlB = "vless://uuid-bbb@server-b.test:8443?sni=b.test&pbk=k-b&sid=s-b"
	id := addServer(t, srv, "Сервер B", urlB, "NL")

	req := httptest.NewRequest(http.MethodPost, "/api/servers/"+id+"/connect", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("/connect = %d: %s", w.Code, w.Body.String())
	}

	got, err := config.ReadSecretKey(secretKeyPath)
	if err != nil {
		t.Fatalf("ReadSecretKey secret.key: %v", err)
	}
	if got != urlB {
		t.Errorf("secret.key содержит %q, ожидался %q", got, urlB)
	}
	t.Logf("secret.key корректно записан: %q", got)
}

// ── Тест 3: handleConnect вызывает InvalidateVLESSCache ──────────────────────
//
// После handleConnect → vlessCache должен быть сброшен.
// Проверяем косвенно: записываем A, парсим (кэш заполнен), затем connect на B
// (handleConnect пишет B + InvalidateVLESSCache), затем парсим — должен быть B.
//
// Внимание: тест намеренно ставит одинаковый mtime на A и B чтобы без
// InvalidateVLESSCache кэш вернул бы A — именно этот сценарий и был багом.
func TestHandleConnect_InvalidatesVLESSCache(t *testing.T) {
	srv, secretKeyPath, cleanup := buildServersServer(t)
	defer cleanup()

	const urlA = "vless://uuid-aaa@cache-a.test:443?sni=cache-a.test&pbk=pk-a&sid=sid-a"
	const urlB = "vless://uuid-bbb@cache-b.test:443?sni=cache-b.test&pbk=pk-b&sid=sid-b"

	// Добавляем оба сервера.
	idA := addServer(t, srv, "Cache A", urlA, "RU")
	idB := addServer(t, srv, "Cache B", urlB, "DE")

	// Подключаем A → secret.key = urlA.
	reqA := httptest.NewRequest(http.MethodPost, "/api/servers/"+idA+"/connect", nil)
	wA := httptest.NewRecorder()
	srv.router.ServeHTTP(wA, reqA)
	if wA.Code != http.StatusOK {
		t.Fatalf("connect A = %d: %s", wA.Code, wA.Body.String())
	}

	// Парсим — кэш заполняется для server-a.test.
	params, err := config.ParseVLESSKeyForTest(secretKeyPath)
	if err != nil {
		t.Fatalf("parseVLESSKey(A): %v", err)
	}
	if params.Address != "cache-a.test" {
		t.Fatalf("после connect A: Address=%q, want cache-a.test", params.Address)
	}
	t.Logf("кэш заполнен: %q", params.Address)

	// Сохраняем mtime A и патчим B тем же mtime — симулируем Windows mtime collision.
	fiA, _ := os.Stat(secretKeyPath)
	mtimeA := fiA.ModTime()

	// Подключаем B → handleConnect пишет urlB + вызывает InvalidateVLESSCache.
	reqB := httptest.NewRequest(http.MethodPost, "/api/servers/"+idB+"/connect", nil)
	wB := httptest.NewRecorder()
	srv.router.ServeHTTP(wB, reqB)
	if wB.Code != http.StatusOK {
		t.Fatalf("connect B = %d: %s", wB.Code, wB.Body.String())
	}

	// Принудительно ставим тот же mtime — без InvalidateVLESSCache кэш вернул бы A.
	if err := os.Chtimes(secretKeyPath, mtimeA, mtimeA); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	// Парсим снова — InvalidateVLESSCache в handleConnect должен был сбросить кэш.
	paramsB, err := config.ParseVLESSKeyForTest(secretKeyPath)
	if err != nil {
		t.Fatalf("parseVLESSKey(B): %v", err)
	}
	if paramsB.Address != "cache-b.test" {
		t.Errorf("после connect B + равный mtime: Address=%q (ожидался cache-b.test) — кэш не был инвалидирован в handleConnect", paramsB.Address)
	} else {
		t.Logf("OK: кэш инвалидирован в handleConnect, B прочитан верно: %q", paramsB.Address)
	}
}

// ── Тест 4: handleConnect 404 на несуществующий сервер ──────────────────────
func TestHandleConnect_NotFound_Returns404(t *testing.T) {
	srv, _, cleanup := buildServersServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/servers/no-such-id-999/connect", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("connect неизвестного ID = %d, ожидался 404", w.Code)
	}
	t.Logf("404 для неизвестного ID: %s", w.Body.String())
}

// ── Тест 5: фронтенд — connectServer читает restart_required ─────────────────
//
// Этот тест проверяет JavaScript-логику косвенно через ответ бэкенда:
// бэкенд возвращает restart_required=true при отсутствии TUN.
// Фронтенд (index.html) обязан читать это поле вместо захардкоженного true.
// Сам JS не запускается в Go, поэтому тест верифицирует контракт API.
func TestHandleConnect_APIContract_RestartRequiredField(t *testing.T) {
	srv, _, cleanup := buildServersServer(t)
	defer cleanup()

	const url1 = "vless://uuid-ccc@contract.test:443?sni=contract.test&pbk=pk-c&sid=sid-c"
	id := addServer(t, srv, "Contract Test", url1, "US")

	req := httptest.NewRequest(http.MethodPost, "/api/servers/"+id+"/connect", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("connect = %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)

	// Убеждаемся что поле restart_required ПРИСУТСТВУЕТ в ответе (не undefined).
	if _, exists := resp["restart_required"]; !exists {
		t.Error("поле restart_required отсутствует в ответе /connect — фронтенд не сможет его прочитать")
	}
	// Убеждаемся что message ПРИСУТСТВУЕТ (фронтенд показывает его при restart_required=false).
	if _, exists := resp["message"]; !exists {
		t.Error("поле message отсутствует в ответе /connect — фронтенд не сможет его показать")
	}
	// success всегда должен быть true при 200.
	if success, _ := resp["success"].(bool); !success {
		t.Error("success != true при HTTP 200")
	}
	t.Logf("API контракт OK: restart_required=%v, message=%q", resp["restart_required"], resp["message"])
}
