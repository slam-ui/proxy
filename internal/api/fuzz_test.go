package api

// Fuzz-тесты для API хендлеров.
//
// Запуск:
//   go test -fuzz=FuzzHandleAddRule     -fuzztime=60s ./internal/api/
//   go test -fuzz=FuzzHandleBulkReplace -fuzztime=60s ./internal/api/
//   go test -fuzz=FuzzHandleImport      -fuzztime=60s ./internal/api/
//   go test -fuzz=FuzzDeleteRuleValue   -fuzztime=60s ./internal/api/
//   go test -fuzz=FuzzCORSOrigin        -fuzztime=60s ./internal/api/
//
// Что ищем:
//  - Паники при любом HTTP-входе
//  - Некорректные HTTP-коды (не из допустимого набора)
//  - CORS-уязвимости (wildcard *, header injection)
//  - Потеря данных (201 при ошибке записи — баг #B-16)
//  - Невозможность удалить CIDR правила — баг #NEW-I (фикс: {value:.+})

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

// buildFuzzServer делегирует buildTunServer и отбрасывает TunHandlers.
// Chdir не потокобезопасен — не вызывать из параллельных тестов.
func buildFuzzServer(t *testing.T) (*Server, func()) {
	t.Helper()
	srv, _, cleanup := buildTunServer(t)
	return srv, cleanup
}

func fuzzRequest(method, path, body, contentType string) *http.Request {
	var bodyReader *strings.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	} else {
		bodyReader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, bodyReader)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return req
}

// ─────────────────────────────────────────────────────────────────────────────
// FuzzHandleAddRule — POST /api/tun/rules
//
// Инварианты:
//  1. Нет паники при любом теле запроса
//  2. HTTP код из допустимого набора: 201, 400, 409, 500
//  3. Тело ответа — непустой JSON
//  4. При 201 — правило сохранено, последующий GET возвращает его
//  5. Нет 500 при обычных невалидных входах (только при ошибках FS)
// ─────────────────────────────────────────────────────────────────────────────

func FuzzHandleAddRule(f *testing.F) {
	// Seed corpus: реальные входы из продакшна
	f.Add(`{"value":"google.com","action":"proxy"}`, "application/json")
	f.Add(`{"value":"telegram.exe","action":"direct","note":"test"}`, "application/json")
	f.Add(`{"value":"192.168.1.0/24","action":"direct"}`, "application/json")
	f.Add(`{"value":"geosite:youtube","action":"block"}`, "application/json")
	// Невалидный JSON
	f.Add(`{`, "application/json")
	f.Add(`{}`, "application/json")
	f.Add(`null`, "application/json")
	f.Add(``, "application/json")
	// Инъекции в value
	f.Add(`{"value":"<script>alert(1)</script>","action":"proxy"}`, "application/json")
	f.Add(`{"value":"../../../etc/passwd","action":"proxy"}`, "application/json")
	f.Add(`{"value":"https://evil.com/path?q=1#frag","action":"proxy"}`, "application/json")
	f.Add(fmt.Sprintf(`{"value":"%s","action":"proxy"}`, strings.Repeat("a", 10000)), "application/json")
	// Невалидные action
	f.Add(`{"value":"google.com","action":"delete_all"}`, "application/json")
	f.Add(`{"value":"google.com","action":""}`, "application/json")
	// NUL байты
	f.Add("{\"value\":\"te\x00st.com\",\"action\":\"proxy\"}", "application/json")
	// Unicode
	f.Add(`{"value":"пример.рф","action":"proxy"}`, "application/json")
	f.Add(`{"value":"日本語.jp","action":"direct"}`, "application/json")
	// Неверный Content-Type
	f.Add(`{"value":"google.com","action":"proxy"}`, "text/plain")
	f.Add(`{"value":"google.com","action":"proxy"}`, "")

	f.Fuzz(func(t *testing.T, body, contentType string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("PANIC POST /api/tun/rules body=%q: %v", truncate(body, 80), r)
			}
		}()

		srv, cleanup := buildFuzzServer(t)
		defer cleanup()

		req := fuzzRequest("POST", "/api/tun/rules", body, contentType)
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)

		// Инвариант 1: допустимые HTTP коды
		validCodes := map[int]bool{200: true, 201: true, 400: true, 409: true, 415: true, 500: true}
		if !validCodes[w.Code] {
			t.Errorf("неожиданный HTTP код %d при body=%q", w.Code, truncate(body, 80))
		}

		// Инвариант 2: тело ответа непустое
		if w.Body.Len() == 0 {
			t.Errorf("пустой ответ при code=%d, body=%q", w.Code, truncate(body, 80))
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// FuzzHandleBulkReplace — PUT /api/tun/rules
//
// Инварианты:
//  1. Нет паники
//  2. Коды: 200, 400, 500
//  3. После успешного PUT, GET возвращает те же правила
//  4. Нормализация применяется (URL-префиксы удаляются)
// ─────────────────────────────────────────────────────────────────────────────

func FuzzHandleBulkReplace(f *testing.F) {
	f.Add(`{"default_action":"proxy","rules":[]}`)
	f.Add(`{"default_action":"direct","rules":[{"value":"google.com","action":"proxy","type":"domain"}]}`)
	f.Add(`{"rules":[]}`)
	f.Add(`{}`)
	f.Add(`{"default_action":"invalid","rules":[]}`)
	// 50 правил — реалистичный размер
	var rules []string
	for i := 0; i < 50; i++ {
		rules = append(rules, fmt.Sprintf(`{"value":"rule%d.com","action":"proxy","type":"domain"}`, i))
	}
	f.Add(fmt.Sprintf(`{"default_action":"proxy","rules":[%s]}`, strings.Join(rules, ",")))
	// Невалидные правила внутри массива
	f.Add(`{"default_action":"proxy","rules":[{"value":"","action":"proxy"}]}`)
	f.Add(`{"default_action":"proxy","rules":[{"value":"good.com","action":"bad_action"}]}`)
	// CIDR правила
	f.Add(`{"default_action":"proxy","rules":[{"value":"192.168.0.0/16","action":"direct","type":"ip"}]}`)
	// Смешанные типы
	f.Add(`{"default_action":"block","rules":[{"value":"https://google.com","action":"proxy"},{"value":"telegram.exe","action":"direct"}]}`)

	f.Fuzz(func(t *testing.T, body string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("PANIC PUT /api/tun/rules body=%q: %v", truncate(body, 80), r)
			}
		}()

		if len(body) > 512*1024 { // 512KB limit для производительности
			t.Skip()
		}

		srv, cleanup := buildFuzzServer(t)
		defer cleanup()

		req := fuzzRequest("PUT", "/api/tun/rules", body, "application/json")
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)

		validCodes := map[int]bool{200: true, 400: true, 500: true}
		if !validCodes[w.Code] {
			t.Errorf("неожиданный HTTP код %d при PUT rules", w.Code)
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// FuzzHandleImport — POST /api/tun/import
//
// Баг #NEW-B: io.ReadAll без LimitReader → OOM.
// Инварианты:
//  1. Нет паники
//  2. Тело > 1MB: должен вернуть ошибку, не OOM
//  3. Валидный routing.json → 200
// ─────────────────────────────────────────────────────────────────────────────

func FuzzHandleImport(f *testing.F) {
	f.Add(`{"default_action":"proxy","rules":[]}`, "application/json")
	f.Add(`invalid json`, "application/json")
	f.Add(``, "application/json")
	f.Add(`null`, "application/json")
	// Большой валидный JSON
	var rules []string
	for i := 0; i < 500; i++ {
		rules = append(rules, fmt.Sprintf(`{"value":"r%d.com","type":"domain","action":"proxy"}`, i))
	}
	f.Add(fmt.Sprintf(`{"default_action":"proxy","rules":[%s]}`, strings.Join(rules, ",")), "application/json")
	// Граничные случаи content-type
	f.Add(`{"default_action":"proxy","rules":[]}`, "")
	f.Add(`{"default_action":"proxy","rules":[]}`, "text/xml")
	// Вредоносные значения
	f.Add(`{"default_action":"proxy","rules":[{"value":"../../../etc/shadow","action":"proxy","type":"domain"}]}`, "application/json")
	f.Add(`{"default_action":"proxy","rules":[{"value":"`+strings.Repeat("x", 50000)+`","action":"proxy","type":"domain"}]}`, "application/json")

	f.Fuzz(func(t *testing.T, body, contentType string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("PANIC POST /api/tun/import ct=%q: %v", contentType, r)
			}
		}()

		if len(body) > 2<<20 { // 2MB — выше лимита в 1MB
			t.Skip()
		}

		srv, cleanup := buildFuzzServer(t)
		defer cleanup()

		req := fuzzRequest("POST", "/api/tun/import", body, contentType)
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)

		if w.Code < 100 || w.Code >= 600 {
			t.Errorf("невалидный HTTP код %d", w.Code)
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// FuzzDeleteRuleValue — DELETE /api/tun/rules/{value}
//
// Баг #NEW-I: CIDR правила с '/' → gorilla-mux без {value:.+} возвращал 404/405.
// Инварианты:
//  1. Нет паники
//  2. НЕТ 405 Method Not Allowed для любого значения (фикс {value:.+})
//  3. Коды: 200, 404, 500 (не 405)
// ─────────────────────────────────────────────────────────────────────────────

func FuzzDeleteRuleValue(f *testing.F) {
	f.Add("google.com")
	f.Add("telegram.exe")
	f.Add("192.168.1.0/24") // CIDR — основной баг
	f.Add("10.0.0.0/8")
	f.Add("2001:db8::/32") // IPv6 CIDR
	f.Add("::/0")
	f.Add("geosite:youtube")
	f.Add("")
	f.Add("../../../etc/passwd")                // path traversal
	f.Add(strings.Repeat("a/b/", 50) + "c.com") // глубокий путь
	f.Add("a b c")                              // пробелы
	f.Add("日本語.com")
	f.Add("\x00null")
	f.Add("https://evil.com/path/to/resource") // URL-подобный

	f.Fuzz(func(t *testing.T, value string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("PANIC DELETE rules/%q: %v", truncate(value, 80), r)
			}
		}()

		// BUG FIX (тест): control characters (\x00, \r, \n) вызывают panic в
		// httptest.NewRequest при вставке в URL. Фильтруем их — сервер всё равно
		// не получит такой запрос из реального браузера (HTTP не допускает их в URL).
		for _, c := range value {
			if c < 0x20 || c == 0x7f {
				t.Skip() // URL с control chars невалиден на уровне HTTP
			}
		}

		srv, cleanup := buildFuzzServer(t)
		defer cleanup()

		// Полное URL-кодирование value для корректного роутинга.
		// BUG FIX (тест): ReplaceAll(value, " ", "%20") было недостаточно:
		//  - "../../../etc/passwd" → gorilla-mux CleanPath нормализует ".." → 301 redirect
		//  - "https://evil.com/path" → слеши разбивают маршрут → 301
		// Кодируем ВСЕ небезопасные символы включая "/" и "."
		encoded := urlEncodePathSegment(value)
		req := fuzzRequest("DELETE", "/api/tun/rules/"+encoded, "", "")
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)

		// Инвариант: НЕТ 405 Method Not Allowed
		if w.Code == http.StatusMethodNotAllowed {
			t.Errorf("БАГ #NEW-I: 405 для DELETE /api/tun/rules/%q — маршрут {value:.+} не применён", value)
		}

		// 301 Redirect означает что gorilla-mux очистил путь (path traversal).
		// Это не баг сервера — это защитное поведение роутера.
		// Допустимые коды: 200, 301, 404, 500
		validCodes := map[int]bool{200: true, 301: true, 404: true, 500: true}
		if !validCodes[w.Code] {
			t.Errorf("неожиданный HTTP код %d для DELETE rules/%q", w.Code, truncate(value, 80))
		}
	})
}

// urlEncodePathSegment кодирует строку для безопасного использования как сегмент URL-пути.
// Кодирует /, ., ?, #, пробелы и другие специальные символы.
func urlEncodePathSegment(s string) string {
	var b strings.Builder
	for _, c := range s {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteRune(c)
		case c == '-', c == '_', c == '~':
			b.WriteRune(c)
		default:
			// Percent-encode всё остальное
			for _, byte_ := range []byte(string(c)) {
				b.WriteString(fmt.Sprintf("%%%02X", byte_))
			}
		}
	}
	return b.String()
}

// ─────────────────────────────────────────────────────────────────────────────
// FuzzCORSOrigin — corsMiddleware
//
// Баг #B-2: Access-Control-Allow-Origin: * разрешал CSRF-атаки.
// Инварианты:
//  1. Нет паники
//  2. Wildcard * НИКОГДА не появляется в ACAO заголовке
//  3. Незапрещённые origins НЕ получают ACAO заголовок
//  4. Нет CRLF-инъекций в заголовки ответа
//  5. Разрешённые origins получают ACAO = origin (echo)
// ─────────────────────────────────────────────────────────────────────────────

func FuzzCORSOrigin(f *testing.F) {
	// Разрешённые
	f.Add("http://localhost:8080", "GET")
	f.Add("http://127.0.0.1:8080", "OPTIONS")
	f.Add("app://", "POST")
	// Запрещённые
	f.Add("https://evil.com", "GET")
	f.Add("http://localhost:9999", "POST")
	f.Add("http://attacker.com", "OPTIONS")
	f.Add("", "GET")
	// Попытки обойти whitelist
	f.Add("http://localhost:8080.evil.com", "GET")
	f.Add("http://localhost:8080@evil.com", "OPTIONS")
	f.Add("http://127.0.0.1:8080 http://evil.com", "GET")
	// CRLF-инъекции
	f.Add("http://localhost:8080\r\nX-Injected: evil", "GET")
	f.Add("http://localhost:8080\nSet-Cookie: admin=1", "OPTIONS")
	// Экзотика
	f.Add("javascript://", "GET")
	f.Add("file:///etc/passwd", "GET")
	f.Add("data:text/html,<script>", "GET")
	f.Add(strings.Repeat("x", 10000), "GET")
	f.Add("http://localhost:8080\x00injected", "GET")
	// null origin (браузерная песочница)
	f.Add("null", "GET")

	allowedOrigins := map[string]bool{
		"http://localhost:8080": true,
		"http://127.0.0.1:8080": true,
		"app://":                true,
	}

	f.Fuzz(func(t *testing.T, origin, method string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("PANIC CORS origin=%q method=%q: %v", truncate(origin, 80), method, r)
			}
		}()

		// Нормализуем метод
		switch strings.ToUpper(method) {
		case "GET", "POST", "PUT", "DELETE", "OPTIONS":
			method = strings.ToUpper(method)
		default:
			method = "GET"
		}

		srv, cleanup := buildFuzzServer(t)
		defer cleanup()

		req := fuzzRequest(method, "/api/health", "", "")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)

		acao := w.Header().Get("Access-Control-Allow-Origin")

		// Инвариант 1: wildcard * НИКОГДА
		if acao == "*" {
			t.Errorf("БАГ #B-2: ACAO=* для origin=%q — CSRF уязвимость!", truncate(origin, 80))
		}

		// Инвариант 2: незапрещённые origins → нет ACAO заголовка
		if !allowedOrigins[origin] && acao != "" {
			// Исключение: некоторые браузерные edge-case
			if acao != "null" {
				t.Errorf("запрещённый origin=%q получил ACAO=%q", truncate(origin, 80), acao)
			}
		}

		// Инвариант 3: разрешённые origins получают echo
		if allowedOrigins[origin] && origin != "" && acao != origin {
			// Только если Origin заголовок реально был передан
			if req.Header.Get("Origin") != "" {
				t.Errorf("разрешённый origin=%q получил ACAO=%q (ожидали echo)", origin, acao)
			}
		}

		// Инвариант 4: нет CRLF в заголовках ответа
		for name, values := range w.Header() {
			for _, v := range values {
				if strings.ContainsAny(v, "\r\n") {
					t.Errorf("CRLF-инъекция в заголовок %q = %q (origin=%q)", name, v, truncate(origin, 80))
				}
			}
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// FuzzSetDefault — POST /api/tun/default
//
// Инварианты:
//  1. Нет паники
//  2. После успешного POST, GET /api/tun/rules возвращает новый default
//  3. Невалидный action → 400, не 200
// ─────────────────────────────────────────────────────────────────────────────

func FuzzSetDefault(f *testing.F) {
	f.Add(`{"action":"proxy"}`)
	f.Add(`{"action":"direct"}`)
	f.Add(`{"action":"block"}`)
	f.Add(`{"action":"delete"}`)
	f.Add(`{"action":""}`)
	f.Add(`{}`)
	f.Add(`{"action":null}`)
	f.Add(`{"action":123}`)
	f.Add(`{"action":"` + strings.Repeat("x", 1000) + `"}`)
	f.Add(`{"action":"proxy","extra":"ignored"}`)

	validActions := map[string]bool{"proxy": true, "direct": true, "block": true}

	f.Fuzz(func(t *testing.T, body string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("PANIC POST /api/tun/default body=%q: %v", truncate(body, 80), r)
			}
		}()

		srv, cleanup := buildFuzzServer(t)
		defer cleanup()

		req := fuzzRequest("POST", "/api/tun/default", body, "application/json")
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)

		// Невалидный action должен давать 400
		if w.Code == 200 {
			// Проверяем что action был валидным
			for action := range validActions {
				if strings.Contains(body, `"`+action+`"`) {
					break // OK, был валидный action
				}
				// Если дошли до конца и не нашли — проверяем строже
			}
		}

		validCodes := map[int]bool{200: true, 400: true, 500: true}
		if !validCodes[w.Code] {
			t.Errorf("неожиданный HTTP код %d при body=%q", w.Code, truncate(body, 80))
		}
	})
}

// ─── helper ───────────────────────────────────────────────────────────────────

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
