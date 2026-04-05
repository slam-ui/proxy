package apprules

// Fuzz-тесты для пакета apprules.
//
// Запуск:
//   go test -fuzz=^FuzzMatcher$          -fuzztime=60s ./internal/apprules/
//   go test -fuzz=FuzzNormalizePattern -fuzztime=60s ./internal/apprules/
//   go test -fuzz=FuzzMatcherSafety    -fuzztime=60s ./internal/apprules/

import (
	"strings"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// FuzzNormalizePattern
//
// Инварианты:
//  1. Нет паники
//  2. Идемпотентность
//  3. Результат в нижнем регистре
//  4. Обратные слеши заменены на прямые
// ─────────────────────────────────────────────────────────────────────────────

func FuzzNormalizePattern(f *testing.F) {
	seeds := []string{
		"telegram.exe", "TELEGRAM.EXE", "Telegram.Exe",
		"C:\\Program Files\\app.exe",
		"c:/program files/app.exe",
		"*/chrome*", "??.exe",
		"", " ", "\t", "\n",
		strings.Repeat("a", 10000),
		"path/to/app.EXE",
		"\x00null\x00byte",
		"日本語.exe",
		"app[1].exe",
		"app{name}.exe",
		"app(name).exe",
		"../../../etc/passwd",
	}

	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, pattern string) {
		// Инвариант 1: нет паники
		result := NormalizePattern(pattern)

		// Инвариант 2: идемпотентность
		result2 := NormalizePattern(result)
		if result != result2 {
			t.Errorf("нарушена идемпотентность: NormalizePattern(%q)=%q, NormalizePattern(%q)=%q",
				pattern, result, result, result2)
		}

		// Инвариант 3: результат в нижнем регистре
		if result != strings.ToLower(result) {
			t.Errorf("результат не в нижнем регистре: NormalizePattern(%q)=%q", pattern, result)
		}

		// Инвариант 4: нет обратных слешей
		if strings.Contains(result, "\\") {
			t.Errorf("результат содержит обратный слеш: NormalizePattern(%q)=%q", pattern, result)
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// FuzzMatcher
//
// Инварианты:
//  1. Нет паники при любых паттерне и значении
//  2. Точное совпадение (после нормализации) → всегда true
//  3. Пустой паттерн и пустое значение → true (точное совпадение пустых строк)
//  4. Симметричность нормализации: Match(NormPat, val) == Match(NormPat, NormVal)
//     не всегда истинна (value нормализуется внутри), но не должно паниковать
// ─────────────────────────────────────────────────────────────────────────────

func FuzzMatcher(f *testing.F) {
	type seed struct{ pattern, value string }
	seeds := []seed{
		{"telegram.exe", "telegram.exe"},
		{"telegram.exe", "TELEGRAM.EXE"},
		{"*chrome*", "google_chrome.exe"},
		{"?.exe", "a.exe"},
		{"*.exe", "some.app.exe"},
		{"", ""},
		{"", "something"},
		{"pattern", ""},
		{"c:/prog*", "C:\\Program Files\\app.exe"},
		{strings.Repeat("*", 100), "a"},
		{"a", strings.Repeat("a", 10000)},
		// Инъекции в wildcard
		{"[abc]", "a"},
		{"[", "a"}, // невалидный glob — не должен паниковать
		{"]", "a"},
		{"[!abc]", "d"},
		// NUL байты
		{"\x00", "\x00"},
		{"pattern\x00ext", "pattern\x00ext"},
		// Unicode
		{"日本語.exe", "日本語.exe"},
		{"*.日本語", "app.日本語"},
	}

	for _, s := range seeds {
		f.Add(s.pattern, s.value)
	}

	m := NewMatcher()

	f.Fuzz(func(t *testing.T, pattern, value string) {
		// Инвариант 1: нет паники
		result := m.Match(pattern, value)
		_ = result

		// Инвариант 2: точное совпадение нормализованных строк → Match true
		normPat := NormalizePattern(pattern)
		normVal := NormalizePattern(value)
		if normPat == normVal {
			if !m.Match(pattern, value) {
				// Исключение: если паттерн содержит невалидные glob символы ([),
				// filepath.Match вернёт error и false даже при равных строках
				if !strings.Contains(normPat, "[") && !strings.Contains(normPat, "]") {
					t.Errorf("точное совпадение должно давать true: pattern=%q value=%q (norm: %q)",
						pattern, value, normPat)
				}
			}
		}

		// Инвариант 3: пустой на пустой → true
		if pattern == "" && value == "" {
			if !m.Match("", "") {
				t.Error("Match(\"\", \"\") должен возвращать true")
			}
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// FuzzMatcherSafety
//
// Проверяем безопасность при злонамеренных паттернах:
// - ReDoS через сложные glob паттерны (filepath.Match линейный, но проверяем)
// - Path traversal в паттернах
// - Переполнение буфера
// - Нулевые байты
// ─────────────────────────────────────────────────────────────────────────────

func FuzzMatcherSafety(f *testing.F) {
	// Специально подобранные опасные входы
	dangerousPatterns := []struct{ p, v string }{
		// Потенциальный ReDoS (для glob — не актуален, но проверяем)
		{"**/**/a", "a/b/c/d/e/f/g/h/a"},
		{"*/*/*/*/*/*", strings.Repeat("a/", 20) + "a"},
		// Очень длинные
		{strings.Repeat("?", 1000), strings.Repeat("a", 1000)},
		{strings.Repeat("*", 500), strings.Repeat("a", 500)},
		// Path traversal
		{"../../../*", "../../../etc/passwd"},
		// Нулевые байты в разных позициях
		{"\x00*.exe", "\x00app.exe"},
		{"app\x00.exe", "app\x00.exe"},
		// Смешанные слеши
		{"c:/prog\\app", "c:/prog\\app"},
		// Очень глубокий путь
		{strings.Repeat("a/", 500) + "*.exe", strings.Repeat("a/", 500) + "app.exe"},
	}

	for _, d := range dangerousPatterns {
		f.Add(d.p, d.v)
	}

	m := NewMatcher()

	f.Fuzz(func(t *testing.T, pattern, value string) {
		// Главная проверка: нет паники и нет зависания
		// (timeout 120s из go test должен поймать зависание)
		done := make(chan bool, 1)
		go func() {
			result := m.Match(pattern, value)
			done <- result
		}()

		// BUG FIX (фаззер): wildcardMatch мог зависать на патологических входах.
		// После добавления защиты от больших входов (n*m > 50M → false),
		// этот таймаут редко срабатывает, но оставляем 10s на случай.
		// Добавляем явный таймаут, чтобы фаззер получил внятный FAIL вместо
		// "fuzzing process hung or terminated unexpectedly: exit status 2".
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("Match зависла более 10 секунд — возможный бесконечный цикл")
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// FuzzMatchAny
//
// MatchAny используется для сопоставления процесса с несколькими паттернами.
// Инварианты:
//  1. Нет паники
//  2. Пустой список паттернов → всегда false
//  3. Если Match(p, v) = true хотя бы для одного p → MatchAny = true
// ─────────────────────────────────────────────────────────────────────────────

func FuzzMatchAny(f *testing.F) {
	f.Add("telegram.exe", "telegram.exe", "chrome.exe", "firefox.exe")
	f.Add("", "a", "b", "c")
	f.Add("*.exe", "*.dll", "*.sys", "app.exe")
	f.Add(strings.Repeat("*", 50), "a", "b", strings.Repeat("a", 100))

	m := NewMatcher()

	f.Fuzz(func(t *testing.T, value, p1, p2, p3 string) {
		patterns := []string{p1, p2, p3}

		// Инвариант 1: нет паники
		result := MatchAny(m, patterns, value)

		// Инвариант 2: пустой список → false
		if MatchAny(m, nil, value) {
			t.Error("MatchAny с nil patterns должен возвращать false")
		}
		if MatchAny(m, []string{}, value) {
			t.Error("MatchAny с пустым списком должен возвращать false")
		}

		// Инвариант 3: если хотя бы один паттерн матчит — результат true
		anyMatch := false
		for _, p := range patterns {
			if m.Match(p, value) {
				anyMatch = true
				break
			}
		}
		if anyMatch != result {
			t.Errorf("MatchAny(%q, %q) = %v, но индивидуальные Match: %v",
				patterns, value, result, anyMatch)
		}
	})
}
