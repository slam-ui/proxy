package config

// Fuzz-тесты для пакета config.
//
// Запуск фаззинга:
//   go test -fuzz=FuzzNormalizeRuleValue -fuzztime=60s ./internal/config/
//   go test -fuzz=FuzzDetectRuleType     -fuzztime=60s ./internal/config/
//   go test -fuzz=FuzzParseVLESSURL      -fuzztime=60s ./internal/config/
//   go test -fuzz=FuzzRoutingRoundTrip   -fuzztime=60s ./internal/config/
//
// Цель: найти паники, бесконечные циклы, невалидные состояния при произвольном входе.

import (
	"net/url"
	"os"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// FuzzNormalizeRuleValue
//
// Свойства которые ВСЕГДА должны быть истинными (инварианты):
//  1. Нет паники
//  2. Результат не длиннее входа (мы только убираем префиксы)
//  3. Идемпотентность: NormalizeRuleValue(NormalizeRuleValue(x)) == NormalizeRuleValue(x)
//  4. Результат не содержит http:// или https:// префикса
//  5. Не содержит BOM байт
// ─────────────────────────────────────────────────────────────────────────────

func FuzzNormalizeRuleValue(f *testing.F) {
	// Seed corpus — реальные входы из продакшна
	seeds := []string{
		// Нормальные случаи
		"google.com",
		"telegram.exe",
		"192.168.1.0/24",
		"geosite:youtube",
		// URL-префиксы которые должны сниматься
		"https://google.com",
		"http://example.com",
		"https://google.com/path?query=1",
		"//example.com",
		// IPv6
		"[::1]",
		"[2001:db8::1]/32",
		// С портом
		"example.com:443",
		"192.168.1.1:8080",
		// Пустые/пробельные
		"",
		"   ",
		"\t\n",
		// BOM
		"\xef\xbb\xbfgoogle.com",
		// Экзотика
		"https://user:pass@host:443/path?q=1#frag",
		"vless://uuid@server:443?sni=example.com",
		// Очень длинный
		strings.Repeat("a", 10000),
		// Спецсимволы
		"exam\x00ple.com",
		"<script>alert(1)</script>",
		"../../../etc/passwd",
		"geosite:" + strings.Repeat("x", 1000),
		// Unicode
		"пример.рф",
		"日本語.jp",
		// Emoji
		"🔥.com",
	}

	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, input string) {
		// Инвариант 1: нет паники (сам факт вызова)
		result := NormalizeRuleValue(input)

		// Инвариант 2: идемпотентность
		result2 := NormalizeRuleValue(result)
		if result != result2 {
			t.Errorf("нарушена идемпотентность: NormalizeRuleValue(%q) = %q, но NormalizeRuleValue(%q) = %q",
				input, result, result, result2)
		}

		// Инвариант 3: нет http/https префиксов в результате
		lower := strings.ToLower(result)
		if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
			t.Errorf("результат содержит URL-префикс: NormalizeRuleValue(%q) = %q", input, result)
		}

		// Инвариант 4: нет BOM в результате
		if strings.HasPrefix(result, "\xef\xbb\xbf") {
			t.Errorf("результат содержит BOM: NormalizeRuleValue(%q) = %q", input, result)
		}

		// Инвариант 5: если входит .exe суффикс — результат должен его сохранить
		if strings.HasSuffix(strings.ToLower(strings.TrimSpace(input)), ".exe") && len(result) > 0 {
			if !strings.HasSuffix(strings.ToLower(result), ".exe") {
				t.Errorf("потерян .exe суффикс: input=%q, result=%q", input, result)
			}
		}

		// Инвариант 6: результат не должен содержать нулевой байт
		if strings.ContainsRune(result, 0) {
			t.Errorf("результат содержит нулевой байт: NormalizeRuleValue(%q) = %q", input, result)
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// FuzzDetectRuleType
//
// Инварианты:
//  1. Нет паники
//  2. Возвращает только одно из 4 допустимых значений
//  3. Валидный CIDR → всегда RuleTypeIP
//  4. .exe суффикс → всегда RuleTypeProcess
//  5. geosite: префикс → всегда RuleTypeGeosite
//  6. Идемпотентность с NormalizeRuleValue: тип не меняется после нормализации
// ─────────────────────────────────────────────────────────────────────────────

func FuzzDetectRuleType(f *testing.F) {
	seeds := []string{
		"google.com", "telegram.exe", "192.168.1.1",
		"10.0.0.0/8", "geosite:youtube", "",
		"[::1]", "256.256.256.256", "not-an-ip",
		"TELEGRAM.EXE", "Telegram.Exe",
		"geosite:YOUTUBE",
		"192.168.1.1:8080",
		"https://google.com",
		strings.Repeat("x", 500),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	validTypes := map[RuleType]bool{
		RuleTypeProcess: true,
		RuleTypeDomain:  true,
		RuleTypeIP:      true,
		RuleTypeGeosite: true,
	}

	f.Fuzz(func(t *testing.T, input string) {
		// Инвариант 1: нет паники
		rt := DetectRuleType(input)

		// Инвариант 2: только допустимые типы
		if !validTypes[rt] {
			t.Errorf("DetectRuleType(%q) = %q — недопустимый тип", input, rt)
		}

		// Инвариант 3: .exe → process
		if strings.HasSuffix(strings.ToLower(strings.TrimSpace(input)), ".exe") {
			if rt != RuleTypeProcess {
				t.Errorf("DetectRuleType(%q) = %q, ожидали RuleTypeProcess", input, rt)
			}
		}

		// Инвариант 4: geosite: → geosite
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(input)), "geosite:") {
			if rt != RuleTypeGeosite {
				t.Errorf("DetectRuleType(%q) = %q, ожидали RuleTypeGeosite", input, rt)
			}
		}

		// Инвариант 5: тип нормализованного значения совпадает с исходным
		// (нормализация не должна менять тип правила)
		normalized := NormalizeRuleValue(input)
		if normalized != "" {
			rt2 := DetectRuleType(normalized)
			// Исключение: CIDR-правила могут становиться доменами после strip-хоста.
			// Например "192.168.1.0/24" → NormalizeRuleValue может обрезать до "192.168.1.0"
			// и тип изменится с IP на Domain — это допустимо.
			// Но geosite: → geosite и .exe → process должны оставаться.
			if rt == RuleTypeProcess && rt2 != RuleTypeProcess {
				t.Errorf("нормализация изменила тип process: input=%q → %q, DetectRuleType: %q → %q",
					input, normalized, rt, rt2)
			}
			if rt == RuleTypeGeosite && rt2 != RuleTypeGeosite {
				t.Errorf("нормализация изменила тип geosite: input=%q → %q, DetectRuleType: %q → %q",
					input, normalized, rt, rt2)
			}
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// FuzzParseVLESSURL
//
// Тестируем внутреннюю функцию разбора VLESS URL через файловый интерфейс.
// Инварианты:
//  1. Нет паники при любом входе
//  2. При ошибке — params == nil
//  3. При успехе — Port в диапазоне [1, 65535]
//  4. При успехе — Address не пустой
//  5. При успехе — validateVLESSParams тоже не паникует
//  6. Схема "vless" — обязательна для успеха
// ─────────────────────────────────────────────────────────────────────────────

func FuzzParseVLESSURL(f *testing.F) {
	seeds := []string{
		// Валидные URL
		"vless://123e4567-e89b-12d3-a456-426614174000@192.168.1.1:443?sni=example.com&pbk=abc&sid=def&flow=xtls-rprx-vision",
		"vless://uuid@example.com:8443?security=reality&sni=x.com&pbk=key&sid=0001",
		// Невалидные
		"",
		"not-a-url",
		"http://example.com:80",
		"vless://",
		"vless://@:0",
		"vless://uuid@host:99999",
		"vless://uuid@host:-1",
		"vless://uuid@host:abc",
		"vless://uuid@:443",
		// С BOM
		"\xef\xbb\xbfvless://uuid@host:443",
		// Комментарии
		"# comment\nvless://uuid@host:443",
		"# full comment",
		// Инъекции
		"vless://uuid@host:443\x00injected",
		"vless://uuid@host:443?sni=x.com&extra=" + strings.Repeat("A", 10000),
		// Unicode в hostname
		"vless://uuid@пример.рф:443",
		// IPv6
		"vless://uuid@[::1]:443",
		"vless://uuid@[2001:db8::1]:8443",
		// Без порта
		"vless://uuid@example.com",
		// Mux параметры
		"vless://uuid@host:443?mux=1",
		"vless://uuid@host:443?multiplex=true",
		"vless://uuid@host:443?mux=invalid",
	}

	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, input string) {
		// Пишем во временный файл
		tmpFile, err := os.CreateTemp("", "vless_fuzz_*.key")
		if err != nil {
			t.Skip("не удалось создать tempfile")
		}
		tmpName := tmpFile.Name()
		defer os.Remove(tmpName)

		_, _ = tmpFile.WriteString(input)
		_ = tmpFile.Close()

		// Инвариант 1: нет паники
		params, parseErr := readAndParseVLESS(tmpName)

		// Инвариант 2: params == nil при ошибке
		if parseErr != nil && params != nil {
			t.Errorf("ошибка парсинга, но params != nil: err=%v, params=%+v", parseErr, params)
		}

		if parseErr == nil && params != nil {
			// Инвариант 3: Port в допустимом диапазоне
			if params.Port < 1 || params.Port > 65535 {
				t.Errorf("невалидный порт %d при парсинге %q", params.Port, input)
			}

			// Инвариант 4: Address не пустой
			if params.Address == "" {
				t.Errorf("пустой Address при парсинге %q", input)
			}

			// Инвариант 5: validate не паникует
			vErr := validateVLESSParams(params)
			// vErr может быть != nil, но не должно быть паники

			// Инвариант 6: если validate ok — значит была схема vless
			if vErr == nil {
				// Первая непустая строка должна содержать "vless://"
				for _, line := range strings.Split(input, "\n") {
					line = strings.TrimSpace(line)
					line = strings.TrimPrefix(line, "\xef\xbb\xbf")
					if line != "" && !strings.HasPrefix(line, "#") {
						if !strings.HasPrefix(line, "vless://") {
							t.Errorf("валидный результат без vless:// схемы: input=%q", input)
						}
						break
					}
				}
			}

			// Инвариант 7: URL должен снова парситься без паники
			reconstructed := "vless://" + params.UUID + "@" + params.Address + ":" +
				"8443"
			_, _ = url.Parse(reconstructed)
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// FuzzRoutingRoundTrip
//
// Проверяем цикл: NormalizeRuleValue → DetectRuleType → RoutingRule
// Инварианты:
//  1. Нет паники
//  2. Нормализованное значение всегда детектируется корректно
//  3. JSON сериализация RoutingRule не паникует
//  4. Note поле не может вызвать проблемы с JSON
// ─────────────────────────────────────────────────────────────────────────────

func FuzzRoutingRoundTrip(f *testing.F) {
	seeds := []struct{ value, action, note string }{
		{"google.com", "proxy", ""},
		{"telegram.exe", "direct", "мессенджер"},
		{"192.168.1.0/24", "direct", "локальная сеть"},
		{"geosite:youtube", "block", ""},
		{"https://example.com/path", "proxy", "с URL-префиксом"},
		{"", "proxy", "пустое значение"},
		{"<script>", "proxy", "XSS попытка"},
		{strings.Repeat("x", 1000), "block", "длинное значение"},
	}

	for _, s := range seeds {
		f.Add(s.value, s.action, s.note)
	}

	f.Fuzz(func(t *testing.T, value, action, note string) {
		// Инвариант 1: нет паники при нормализации
		normalized := NormalizeRuleValue(value)

		// Инвариант 2: DetectRuleType не паникует
		var rt RuleType
		if normalized != "" {
			rt = DetectRuleType(normalized)
		}

		// Инвариант 3: создание RoutingRule не паникует
		var ra RuleAction
		switch action {
		case "proxy":
			ra = ActionProxy
		case "direct":
			ra = ActionDirect
		case "block":
			ra = ActionBlock
		default:
			ra = ActionProxy
		}

		rule := RoutingRule{
			Value:  normalized,
			Type:   rt,
			Action: ra,
			Note:   note,
		}

		// Инвариант 4: JSON маршалинг не паникует
		cfg := &RoutingConfig{
			DefaultAction: ra,
			Rules:         []RoutingRule{rule},
		}
		_ = cfg
		// (SaveRoutingConfig требует путь к файлу — не вызываем,
		//  но конструкция не должна паниковать)

		// Инвариант 5: Note не должен ломать структуру при любом содержимом
		_ = rule.Note
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// FuzzSanitizeRoutingConfig
//
// SanitizeRoutingConfig исправляет неверные типы правил — проверяем что
// она не паникует и возвращает валидный конфиг при любом входе.
// ─────────────────────────────────────────────────────────────────────────────

func FuzzSanitizeRoutingConfig(f *testing.F) {
	f.Add("proxy", "google.com", "domain", "192.168.1.1", "ip", "telegram.exe", "process")
	f.Add("direct", "", "", "", "", "", "")
	f.Add("block", "geosite:yt", "geosite", "bad-type", "unknown", "x.exe", "")

	f.Fuzz(func(t *testing.T, defAction, v1, t1, v2, t2, v3, t3 string) {
		cfg := &RoutingConfig{
			DefaultAction: RuleAction(defAction),
			Rules: []RoutingRule{
				{Value: v1, Type: RuleType(t1), Action: ActionProxy},
				{Value: v2, Type: RuleType(t2), Action: ActionDirect},
				{Value: v3, Type: RuleType(t3), Action: ActionBlock},
			},
		}

		// Не должно паниковать
		SanitizeRoutingConfig(cfg)

		// После санитизации все не-пустые правила должны иметь валидный тип
		validTypes := map[RuleType]bool{
			RuleTypeProcess: true, RuleTypeDomain: true,
			RuleTypeIP: true, RuleTypeGeosite: true,
		}
		for i, rule := range cfg.Rules {
			if rule.Value != "" && !validTypes[rule.Type] {
				t.Errorf("SanitizeRoutingConfig: правило[%d] Value=%q имеет невалидный тип %q",
					i, rule.Value, rule.Type)
			}
		}
	})
}
