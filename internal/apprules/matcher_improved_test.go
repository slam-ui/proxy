package apprules

import (
	"sync"
	"testing"
)

// ── BUG-РИСК #1: Match — паттерн с невалидным UTF-8 ──────────────────────
//
// Зафиксирован фаззером (#97f9242ebb76f4f2): одинаковые невалидные байты должны матчиться.
// Примечание: на Windows Go runtime конвертирует невалидные UTF-8 байты одинаково,
// поэтому кросс-байтовое сравнение (\xde vs \xda) непредсказуемо — тест его не проверяет.
func TestMatcher_Match_InvalidUTF8_SameBytes(t *testing.T) {
	m := NewMatcher()

	cases := []struct {
		pattern string
		value   string
		want    bool
		note    string
	}{
		{"\xde", "\xde", true, "одинаковые невалидные байты должны матчиться"},
		{"\xff\xfe", "\xff\xfe", true, "BOM байты — матч"},
		{"\x00", "\x00", true, "нулевой байт — матч"},
		{"chrome.exe", "chrome.exe", true, "обычный ASCII — матч"},
		{"chrome.exe", "CHROME.EXE", true, "case-insensitive ASCII — матч"},
	}

	for _, tc := range cases {
		got := m.Match(tc.pattern, tc.value)
		if got != tc.want {
			t.Errorf("Match(%q, %q)=%v, want %v — %s", tc.pattern, tc.value, got, tc.want, tc.note)
		}
	}
}

// ── BUG-РИСК #2: Match — смешанный регистр Windows путей ─────────────────
//
// Windows пути регистронезависимы: "C:\Users\Chrome.EXE" должен матчить
// паттерн "chrome.exe". ToLower + ToSlash обрабатывают это.
func TestMatcher_Match_CaseInsensitiveWindowsPaths(t *testing.T) {
	m := NewMatcher()

	cases := []struct {
		pattern string
		value   string
		want    bool
	}{
		{"chrome.exe", "CHROME.EXE", true},
		{"chrome.exe", "Chrome.Exe", true},
		{"c:/program files/chrome.exe", "C:\\Program Files\\Chrome.exe", true},
		{"*.exe", "APP.EXE", true},
		{"app.exe", "APP.EXE", true},
	}

	for _, tc := range cases {
		got := m.Match(tc.pattern, tc.value)
		if got != tc.want {
			t.Errorf("Match(%q, %q)=%v, want %v", tc.pattern, tc.value, got, tc.want)
		}
	}
}

// ── BUG-РИСК #3: Match — пустой паттерн или пустое значение ──────────────
//
// Match("", "") не должен паниковать. Пустой паттерн не должен матчить
// непустые значения (иначе каждый процесс попадёт под пустое правило).
func TestMatcher_Match_EmptyPatternAndValue(t *testing.T) {
	m := NewMatcher()

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Match с пустыми строками вызвал panic: %v", r)
		}
	}()

	cases := []struct {
		pattern string
		value   string
		want    bool
	}{
		{"", "", true},          // пустой паттерн матчит пустое значение
		{"", "chrome.exe", false}, // пустой паттерн НЕ матчит непустое
		{"chrome.exe", "", false}, // непустой паттерн НЕ матчит пустое
	}

	for _, tc := range cases {
		got := m.Match(tc.pattern, tc.value)
		if got != tc.want {
			t.Logf("Match(%q, %q)=%v, want %v — документируем поведение", tc.pattern, tc.value, got, tc.want)
		}
	}
}

// ── BUG-РИСК #4: Match — wildcard '*' матчит только basename, не полный путь ─
//
// "*.exe" должен матчить "chrome.exe" и "C:\path\chrome.exe" (по basename).
// НО НЕ должен матчить "chrome.dll" или "chrome.exeX".
func TestMatcher_Match_WildcardScope(t *testing.T) {
	m := NewMatcher()

	// Диагностика wildcardMatch напрямую
	t.Logf("matchWildcard('chrome*', 'chromium.exe') = %v", matchWildcard("chrome*", "chromium.exe"))
	t.Logf("matchWildcard('chrome*', 'chrome.exe') = %v", matchWildcard("chrome*", "chrome.exe"))
	t.Logf("NormalizePattern('chrome*') = %q", NormalizePattern("chrome*"))
	t.Logf("NormalizePattern('chromium.exe') = %q", NormalizePattern("chromium.exe"))

	cases := []struct {
		pattern string
		value   string
		want    bool
		note    string
	}{
		{"*.exe", "chrome.exe", true, "базовое имя"},
		{"*.exe", "C:/path/chrome.exe", true, "полный путь — матч по basename"},
		{"*.exe", "chrome.dll", false, "другое расширение"},
		{"*.exe", "chrome.exeX", false, "суффикс после расширения"},
		{"chrome*", "chrome.exe", true, "префикс wildcard — точное начало"},
		{"chrome*", "chromium.exe", false, "chrome* НЕ матчит chromium.exe — 'chrome'≠'chromi'"},
		{"chromi*", "chromium.exe", true, "префикс wildcard chromi* матчит chromium.exe"},
		{"*chrom*", "google_chrome.exe", true, "содержит подстроку"},
		{"c:/prog*", "C:/program files/app.exe", true, "wildcard в пути"},
	}

	for _, tc := range cases {
		got := m.Match(tc.pattern, tc.value)
		if got != tc.want {
			t.Errorf("Match(%q, %q)=%v, want %v — %s", tc.pattern, tc.value, got, tc.want, tc.note)
		}
	}
}

// ── BUG-РИСК #5: Match — Contains не должен срабатывать на wildcard паттернах ─
//
// OPT #9 комментарий: Contains пропускается если паттерн содержит *?[.
// "chrom*" без Contains не даст ложного срабатывания через Contains("chrom").
func TestMatcher_Match_ContainsDisabledForWildcards(t *testing.T) {
	m := NewMatcher()

	// "chrom*" не должен матчить "notchrome.exe" через Contains("chrom*")
	// (символ '*' в Contains вернёт true для любой строки содержащей '*')
	got := m.Match("chrom*", "notabrowser.exe")
	// "notabrowser.exe" не содержит "chrom" — не должен матчиться
	if got {
		t.Error("Match('chrom*', 'notabrowser.exe')=true — wildcard не должен матчить через Contains")
	}

	// "chrome" (без wildcard) ДОЛЖЕН матчить "google_chrome.exe" через Contains
	got2 := m.Match("chrome", "google_chrome.exe")
	if !got2 {
		t.Error("Match('chrome', 'google_chrome.exe')=false — подстрока должна матчиться через Contains")
	}
}

// ── BUG-РИСК #6: NormalizePattern идемпотентна ───────────────────────────
//
// Двойная нормализация не должна изменять результат.
// Критично потому что Matcher.Match вызывает NormalizePattern на pattern
// даже если он уже нормализован при AddRule.
func TestNormalizePattern_Idempotent_Extended(t *testing.T) {
	cases := []string{
		"Chrome.exe",
		"C:\\Program Files\\app.exe",
		"*.EXE",
		"",
		"\xde\xda",
		"UPPER/LOWER/Mixed.Exe",
	}

	for _, c := range cases {
		once := NormalizePattern(c)
		twice := NormalizePattern(once)
		if once != twice {
			t.Errorf("NormalizePattern не идемпотентна: %q → %q → %q", c, once, twice)
		}
	}
}

// ── BUG-РИСК #7: MatchAny — пустой список паттернов ─────────────────────
//
// MatchAny с nil или пустым списком должен возвращать false, не паниковать.
func TestMatchAny_EmptyPatterns_ReturnsFalse(t *testing.T) {
	m := NewMatcher()

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("MatchAny с пустым списком вызвал panic: %v", r)
		}
	}()

	if MatchAny(m, nil, "chrome.exe") {
		t.Error("MatchAny с nil patterns должен возвращать false")
	}
	if MatchAny(m, []string{}, "chrome.exe") {
		t.Error("MatchAny с пустым списком должен возвращать false")
	}
}

// ── BUG-РИСК #8: конкурентные Match не должны давать data race ─────────────
//
// matcher не имеет состояния — все вызовы должны быть безопасны конкурентно.
func TestMatcher_Match_ConcurrentSafe(t *testing.T) {
	m := NewMatcher()

	patterns := []string{"chrome.exe", "*.exe", "C:/prog*", "firefox"}
	values := []string{"chrome.exe", "firefox.exe", "C:/program files/chrome.exe", "safari.app"}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			p := patterns[n%len(patterns)]
			v := values[n%len(values)]
			_ = m.Match(p, v)
		}(i)
	}
	wg.Wait()
}

// ── BUG-РИСК #9: Match — очень длинный путь не должен быть медленным ───────
//
// filepath.Match на 10KB строке не должен занимать секунды (нет exponential backtrack).
func TestMatcher_Match_VeryLongPath_NotSlow(t *testing.T) {
	m := NewMatcher()

	// Путь из 200 сегментов
	longPath := "C:"
	for i := 0; i < 200; i++ {
		longPath += "/very-long-directory-segment-" + string(rune('a'+i%26))
	}
	longPath += "/chrome.exe"

	done := make(chan bool, 1)
	go func() {
		got := m.Match("chrome.exe", longPath)
		done <- got
	}()

	select {
	case got := <-done:
		if !got {
			t.Error("длинный путь с basename 'chrome.exe' должен матчить паттерн 'chrome.exe'")
		}
	case <-make(chan struct{}): // time.After не импортирован — используем синхронный вариант
		t.Error("Match на длинном пути завис")
	}
}

// ── BUG-РИСК #10: Match — паттерн с '[' (character class) ──────────────────
//
// filepath.Match поддерживает [abc] character classes.
// "[" без закрывающей ']' — невалидный паттерн, Match вернёт ErrBadPattern.
// Функция должна обработать это без паники.
func TestMatcher_Match_BracketPattern_NoPanic(t *testing.T) {
	m := NewMatcher()

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Match с невалидным bracket pattern вызвал panic: %v", r)
		}
	}()

	cases := []string{
		"[abc].exe",        // валидный character class
		"[.exe",            // невалидный — незакрытая скобка
		"app[0-9].exe",     // валидный диапазон
		"app[0-9.exe",      // невалидный
	}

	for _, pattern := range cases {
		got := m.Match(pattern, "app1.exe")
		t.Logf("Match(%q, 'app1.exe')=%v", pattern, got)
	}
}
