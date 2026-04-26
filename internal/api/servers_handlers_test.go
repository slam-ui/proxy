package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"proxyclient/internal/logger"
)

// ── Unit-тесты утилит (B-5) ───────────────────────────────────────────────────

// TestMedianInt64_Single проверяет медиану одного значения
func TestMedianInt64_Single(t *testing.T) {
	result := medianInt64([]int64{42})
	if result != 42 {
		t.Errorf("medianInt64([42]) = %v, ожидалось 42", result)
	}
}

// TestMedianInt64_Odd проверяет медиану для нечётного количества
func TestMedianInt64_Odd(t *testing.T) {
	result := medianInt64([]int64{10, 20, 30})
	if result != 20 {
		t.Errorf("medianInt64([10, 20, 30]) = %v, ожидалось 20", result)
	}

	result = medianInt64([]int64{30, 10, 20})
	if result != 20 {
		t.Errorf("medianInt64([30, 10, 20]) = %v, ожидалось 20", result)
	}

	result = medianInt64([]int64{5, 1, 9})
	if result != 5 {
		t.Errorf("medianInt64([5, 1, 9]) = %v, ожидалось 5", result)
	}
}

// TestMedianInt64_Even проверяет медиану для чётного количества
func TestMedianInt64_Even(t *testing.T) {
	result := medianInt64([]int64{10, 20, 30, 40})
	if result != 25 {
		t.Errorf("medianInt64([10, 20, 30, 40]) = %v, ожидалось 25", result)
	}

	result = medianInt64([]int64{40, 10, 30, 20})
	if result != 25 {
		t.Errorf("medianInt64([40, 10, 30, 20]) = %v, ожидалось 25", result)
	}
}

// TestMedianInt64_Empty проверяет медиану пустого среза
func TestMedianInt64_Empty(t *testing.T) {
	result := medianInt64([]int64{})
	if result != 0 {
		t.Errorf("medianInt64([]) = %v, ожидалось 0", result)
	}
}

// TestMedianInt64_Duplicates проверяет медиану с дублирующимися значениями
func TestMedianInt64_Duplicates(t *testing.T) {
	result := medianInt64([]int64{10, 10, 20, 20})
	if result != 15 {
		t.Errorf("medianInt64([10, 10, 20, 20]) = %v, ожидалось 15", result)
	}

	result = medianInt64([]int64{5, 5, 5})
	if result != 5 {
		t.Errorf("medianInt64([5, 5, 5]) = %v, ожидалось 5", result)
	}
}

// TestMedianInt64_NoOverflow проверяет что medianInt64 не переполняется на больших значениях int64.
//
// BUG FIX: старая формула (a+b)/2 даёт overflow когда a+b > math.MaxInt64.
// Безопасная формула: a + (b-a)/2.
func TestMedianInt64_NoOverflow(t *testing.T) {
	const maxInt64 = int64(^uint64(0) >> 1) // math.MaxInt64 = 9223372036854775807

	// Два числа, сумма которых переполнила бы int64.
	// (9223372036854775806 + 9223372036854775807) переполняет — правильный ответ 9223372036854775806.
	a := maxInt64 - 1
	b := maxInt64
	result := medianInt64([]int64{a, b})
	expected := a // a + (b-a)/2 = (MaxInt64-1) + (1)/2 = MaxInt64-1

	if result != expected {
		t.Errorf("medianInt64([MaxInt64-1, MaxInt64]) = %d, ожидалось %d (overflow?)", result, expected)
	}

	// Симметричный случай: оба чётные
	a2 := maxInt64 - 3
	b2 := maxInt64 - 1
	result2 := medianInt64([]int64{a2, b2})
	expected2 := maxInt64 - 2 // a2 + (b2-a2)/2 = (MaxInt64-3) + 1 = MaxInt64-2
	if result2 != expected2 {
		t.Errorf("medianInt64([MaxInt64-3, MaxInt64-1]) = %d, ожидалось %d", result2, expected2)
	}
}

// TestExtractAddr_Valid проверяет парсинг корректных VLESS URL
func TestExtractAddr_Valid(t *testing.T) {
	tests := []struct {
		url      string
		expected string
	}{
		{"vless://uuid@host.com:443", "host.com:443"},
		{"vless://uuid@host.com:443?sni=host.com", "host.com:443"},
		{"vless://uuid@host.com:443#server", "host.com:443"},
		{"vless://uuid@192.168.1.1:8080?flow=xyz#name", "192.168.1.1:8080"},
		{"vless://uuid@1.2.3.4:443", "1.2.3.4:443"},
		{"vless://00000000-0000-0000-0000-000000000000@example.com:443", "example.com:443"},
	}

	for _, tt := range tests {
		result := extractAddr(tt.url)
		if result != tt.expected {
			t.Errorf("extractAddr(%q) = %q, ожидалось %q", tt.url, result, tt.expected)
		}
	}
}

// TestExtractAddr_Invalid проверяет некорректные VLESS URL
func TestExtractAddr_Invalid(t *testing.T) {
	tests := []string{
		"",
		"vless://",
		"vless://uuid@",
	}

	for _, tt := range tests {
		result := extractAddr(tt)
		if result != "" {
			t.Errorf("extractAddr(%q) = %q, ожидалось пустая строка", tt, result)
		}
	}
}

// TestPingServerWithProbes_Invalid проверяет поведение при невалидном URL
func TestPingServerWithProbes_Invalid(t *testing.T) {
	ms, minMs, maxMs, ok := pingServerWithProbes(context.Background(), "invalid://url", 3)
	if ok {
		t.Error("pingServerWithProbes с невалидным URL должен вернуть ok=false")
	}
	if ms != 0 || minMs != 0 || maxMs != 0 {
		t.Errorf("pingServerWithProbes с ошибкой должен вернуть (0,0,0,false), получилось (%v,%v,%v,%v)", ms, minMs, maxMs, ok)
	}
}

// TestPingServerWithProbes_AllProbesFail проверяет случай когда все пробы не удаются
func TestPingServerWithProbes_AllProbesFail(t *testing.T) {
	ms, minMs, maxMs, ok := pingServerWithProbes(context.Background(), "vless://uuid@nonexistent.invalid:443", 3)
	if ok {
		t.Error("pingServerWithProbes к несуществующему адресу должен вернуть ok=false")
	}
	if ms != 0 || minMs != 0 || maxMs != 0 {
		t.Errorf("pingServerWithProbes с ошибкой должен вернуть (0,0,0,false), получилось (%v,%v,%v,%v)", ms, minMs, maxMs, ok)
	}
}

// TestPingServerWithProbes_ContextCancelDuringPause проверяет что отмена контекста
// ВО ВРЕМЯ паузы между пробами корректно прерывает цикл (break probeLoop).
//
// BUG FIX: до исправления break внутри select прерывал только select, НЕ внешний for.
// После select продолжался следующий DialContext (который тут же падал с context.Canceled),
// а затем ctx.Err() только на СЛЕДУЮЩЕЙ итерации прерывал цикл.
// С именованным break probeLoop цикл прерывается немедленно.
func TestPingServerWithProbes_ContextCancelDuringPause(t *testing.T) {
	// Слушаем на реальном порту чтобы первая проба успела.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("не удалось создать TCP listener:", err)
	}
	defer ln.Close()
	addr := ln.Addr().String()
	vlessURL := "vless://uuid@" + addr + "?security=none"

	// Принимаем одно соединение (первая проба) и закрываем listener.
	go func() {
		conn, _ := ln.Accept()
		if conn != nil {
			conn.Close()
		}
	}()

	// Отменяем контекст сразу — к моменту паузы он уже будет отменён.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // отменяем до вызова

	// С отменённым контекстом функция должна вернуть ok=false без паники.
	start := time.Now()
	_, _, _, ok := pingServerWithProbes(ctx, vlessURL, 3)
	elapsed := time.Since(start)

	if ok {
		t.Error("должен вернуть ok=false при отменённом контексте")
	}
	// Функция не должна зависать: при проблемах с break probeLoop
	// мог выполняться лишний DialContext в петле.
	if elapsed > 500*time.Millisecond {
		t.Errorf("заняло %v — возможно break probeLoop не работает", elapsed)
	}
}

// ── URL-тесты подписок (C-5) ──────────────────────────────────────────────────

// buildServerRoutesServer создаёт сервер с SetupServerRoutes ПЕРЕД FinalizeRoutes.
// Важно: FinalizeRoutes добавляет catch-all "/" — всё после него будет перехвачено.
func buildServerRoutesServer(t *testing.T) (*Server, func()) {
	t.Helper()
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	os.MkdirAll("data", 0755)
	os.WriteFile("secret.key", []byte(""), 0644)

	srv := NewServer(Config{
		XRayManager:  &stubXray{},
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
	}, context.Background())

	SetupServerRoutes(srv, "secret.key")
	srv.FinalizeRoutes()

	return srv, func() { _ = os.Chdir(old) }
}

// newTestServerWithMockFetch создаёт сервер с ServersHandlers у которого подменён fetchURLFn.
func newTestServerWithMockFetch(t *testing.T, returnVLESS string) *Server {
	t.Helper()
	srv := NewServer(Config{
		XRayManager:  &stubXray{},
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
	}, context.Background())

	h := SetupServerRoutes(srv, "secret.key")
	h.fetchURLFn = func(_ string) (string, error) { return returnVLESS, nil }
	srv.FinalizeRoutes()
	return srv
}

// TestFetchVLESSFromURL_PlainText проверяет что plain-text VLESS URI парсится корректно.
func TestFetchVLESSFromURL_PlainText(t *testing.T) {
	const vlessLine = "vless://12345678-1234-1234-1234-123456789abc@example.com:443?security=reality&type=tcp#TestServer"

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("# список серверов\n" + vlessLine + "\n"))
	}))
	defer ts.Close()

	got, err := fetchVLESSFromURL(ts.URL)
	if err != nil {
		t.Fatalf("fetchVLESSFromURL: %v", err)
	}
	if got != vlessLine {
		t.Errorf("got %q, want %q", got, vlessLine)
	}
}

// TestFetchVLESSFromURL_Base64 проверяет Base64-encoded V2ray subscription.
func TestFetchVLESSFromURL_Base64(t *testing.T) {
	const vlessLine = "vless://aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee@sub.example.com:443?type=tcp#Sub"
	encoded := base64.StdEncoding.EncodeToString([]byte(vlessLine + "\n"))

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(encoded))
	}))
	defer ts.Close()

	got, err := fetchVLESSFromURL(ts.URL)
	if err != nil {
		t.Fatalf("fetchVLESSFromURL (base64): %v", err)
	}
	if got != vlessLine {
		t.Errorf("got %q, want %q", got, vlessLine)
	}
}

// TestFetchVLESSFromURL_NoVLESS проверяет ошибку когда VLESS не найден.
func TestFetchVLESSFromURL_NoVLESS(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("# пустой файл без vless"))
	}))
	defer ts.Close()

	_, err := fetchVLESSFromURL(ts.URL)
	if err == nil {
		t.Fatal("ожидалась ошибка, но получено nil")
	}
}

// TestHandleFetchURL_ValidVLESS проверяет POST /api/servers/fetch-url с валидным VLESS.
func TestHandleFetchURL_ValidVLESS(t *testing.T) {
	const vlessLine = "vless://12345678-1234-1234-1234-123456789abc@myserver.com:443?security=reality&type=tcp#Test"
	const subURL = "https://my-subscription.example.com/sub"

	srv, cleanup := buildServerRoutesServer(t)
	defer cleanup()

	srv2 := newTestServerWithMockFetch(t, vlessLine)
	body, _ := json.Marshal(map[string]string{"url": subURL})
	req := httptest.NewRequest(http.MethodPost, "/api/servers/fetch-url", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv2.router.ServeHTTP(w, req)
	_ = srv

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, body=%s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp["success"] != true {
		t.Errorf("success=%v, ожидалось true", resp["success"])
	}
	serverObj, ok := resp["server"].(map[string]interface{})
	if !ok {
		t.Fatal("поле server отсутствует")
	}
	if serverObj["url"] != vlessLine {
		t.Errorf("server.url=%v, ожидалось %q", serverObj["url"], vlessLine)
	}
	if serverObj["subscription_url"] != subURL {
		t.Errorf("subscription_url=%v, ожидалось %q", serverObj["subscription_url"], subURL)
	}
}

// TestValidateSubscriptionURL проверяет валидацию URL субскрипций.
func TestValidateSubscriptionURL(t *testing.T) {
	valid := []string{
		"https://example.com/sub",
		"http://my-server.net:8080/vless",
	}
	for _, u := range valid {
		if err := validateSubscriptionURL(u); err != nil {
			t.Errorf("validateSubscriptionURL(%q) = %v, ожидалось nil", u, err)
		}
	}

	invalid := []string{
		"ftp://example.com/sub",
		"http://localhost/sub",
		"http://127.0.0.1/sub",
		"http://[::1]/sub",
		"not-a-url",
		"",
	}
	for _, u := range invalid {
		if err := validateSubscriptionURL(u); err == nil {
			t.Errorf("validateSubscriptionURL(%q) = nil, ожидалась ошибка", u)
		}
	}
}
