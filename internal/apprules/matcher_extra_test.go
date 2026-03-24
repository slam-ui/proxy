package apprules

import (
	"testing"
)

// ─── NormalizePattern ──────────────────────────────────────────────────────

func TestNormalizePattern_LowercasesInput(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Chrome.EXE", "chrome.exe"},
		{"FIREFOX.EXE", "firefox.exe"},
		{"My App.exe", "my app.exe"},
		{"already_lower.exe", "already_lower.exe"},
	}
	for _, tc := range cases {
		got := NormalizePattern(tc.in)
		if got != tc.want {
			t.Errorf("NormalizePattern(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestNormalizePattern_ForwardSlashUnchanged проверяет что пути с прямыми слешами
// (уже нормализованные) не меняются — это кросс-платформенное поведение.
// Конвертация обратных слешей → прямые выполняется filepath.ToSlash только на Windows;
// Windows-специфичный тест см. в matcher_windows_test.go.
func TestNormalizePattern_ForwardSlashUnchanged(t *testing.T) {
	cases := []struct{ in, want string }{
		{"c:/program files/app.exe", "c:/program files/app.exe"},
		{"c:/windows/system32/cmd.exe", "c:/windows/system32/cmd.exe"},
		{"app.exe", "app.exe"},
		{"*.exe", "*.exe"},
	}
	for _, tc := range cases {
		got := NormalizePattern(tc.in)
		if got != tc.want {
			t.Errorf("NormalizePattern(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizePattern_EmptyString(t *testing.T) {
	if got := NormalizePattern(""); got != "" {
		t.Errorf("NormalizePattern(\"\") = %q, want empty", got)
	}
}

func TestNormalizePattern_Idempotent(t *testing.T) {
	inputs := []string{"chrome.exe", "c:/program files/app.exe", "*.exe", "my_app"}
	for _, input := range inputs {
		once := NormalizePattern(input)
		twice := NormalizePattern(once)
		if once != twice {
			t.Errorf("NormalizePattern не идемпотентна для %q: %q → %q", input, once, twice)
		}
	}
}

// ─── Matcher: полные пути Windows ─────────────────────────────────────────

// TestMatcher_WindowsFullPath_MatchesByBasename проверяет что паттерн по basename
// работает с полными Windows-путями в value. Паттерны — нормализованные (lowercase,
// без бэкслешей). Тест прохода полного пути как паттерна — в matcher_windows_test.go,
// т.к. filepath.Base на Linux не понимает бэкслеши как разделители.
func TestMatcher_WindowsFullPath_MatchesByBasename(t *testing.T) {
	m := NewMatcher()
	cases := []struct {
		pattern, value string
		want           bool
	}{
		// Простое имя файла совпадает с value — полным Windows-путём
		{"chrome.exe", `C:\Program Files\Google\Chrome\chrome.exe`, true},
		{"cmd.exe", `C:\Windows\System32\cmd.exe`, true},
		// Не то приложение
		{"chrome.exe", `C:\Program Files\Mozilla\firefox.exe`, false},
	}
	for _, tc := range cases {
		got := m.Match(tc.pattern, tc.value)
		if got != tc.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tc.pattern, tc.value, got, tc.want)
		}
	}
}

// TestMatcher_WildcardFullPath проверяет что wildcard работает по basename.
func TestMatcher_WildcardFullPath(t *testing.T) {
	m := NewMatcher()
	// *.exe должен совпадать с полным путём по basename
	if !m.Match("*.exe", `C:\Program Files\app.exe`) {
		t.Error("*.exe должен совпадать с полным путём через basename")
	}
	if m.Match("*.dll", `C:\Program Files\app.exe`) {
		t.Error("*.dll не должен совпадать с .exe")
	}
}

// ─── Matcher: паттерны без wildcard не используют Contains ────────────────

func TestMatcher_WildcardPattern_NoContainsFallback(t *testing.T) {
	m := NewMatcher()
	// "chrom*" — имеет wildcard-символ '*', Contains не должен применяться
	// "chrom*" должен совпасть с "chromium.exe" через glob
	if !m.Match("chrom*", "chromium.exe") {
		t.Error("chrom* должен совпасть с chromium.exe через glob")
	}
}

func TestMatcher_SpecialCharsInPattern_NoFalsePositive(t *testing.T) {
	m := NewMatcher()
	// Паттерн содержит '*' — не должен попасть в ветку Contains
	if m.Match("chrome*", "notchrome.exe") {
		t.Error("chrome* не должен совпасть с notchrome.exe")
	}
}

// ─── Matcher: регистронезависимость во всех ветках ────────────────────────

// TestMatcher_CaseInsensitive_AllBranches проверяет регистронезависимость во всех ветках.
//
// Контракт Match(): паттерн ДОЛЖЕН быть уже нормализован через NormalizePattern
// (engine.AddRule делает это при сохранении). Нормализации подвергается только VALUE —
// именно она приходит из ОС в произвольном регистре. Поэтому:
//   - паттерны в тестах: уже lowercase/forward-slash
//   - value: в верхнем регистре — реальный сценарий с Windows-путями
func TestMatcher_CaseInsensitive_AllBranches(t *testing.T) {
	m := NewMatcher()
	cases := []struct {
		name    string
		pattern string // нормализован: lowercase, forward-slash
		value   string // из ОС: произвольный регистр
		want    bool
	}{
		// Ветка точного совпадения: value в верхнем регистре
		{"exact: uppercase value", "chrome.exe", "CHROME.EXE", true},
		// Ветка wildcard: value в верхнем регистре
		{"wildcard: uppercase value", "*.exe", "CHROME.EXE", true},
		// Ветка contains: value в верхнем регистре
		{"contains: uppercase value", "chrome", "CHROME.EXE", true},
		// Ветка basename: полный путь Windows в верхнем регистре
		{"basename: uppercase Windows path", "chrome.exe", `C:\CHROME.EXE`, true},
	}
	for _, tc := range cases {
		got := m.Match(tc.pattern, tc.value)
		if got != tc.want {
			t.Errorf("[%s] Match(%q, %q) = %v, want %v", tc.name, tc.pattern, tc.value, got, tc.want)
		}
	}
}

// ─── Matcher: MatchAny дополнительные сценарии ─────────────────────────────

func TestMatchAny_FirstMatchWins(t *testing.T) {
	m := NewMatcher()
	// Оба паттерна совпадают, но MatchAny возвращает true (без разбора который первый)
	if !MatchAny(m, []string{"chrome.exe", "chrome"}, "chrome.exe") {
		t.Error("MatchAny должен вернуть true если хотя бы один паттерн совпадает")
	}
}

func TestMatchAny_EmptyValue(t *testing.T) {
	m := NewMatcher()
	// Пустое значение не должно совпасть с ни с чем
	if MatchAny(m, []string{"chrome.exe", "*.exe"}, "") {
		t.Error("MatchAny не должен совпасть с пустым value")
	}
}

func TestMatchAny_SinglePattern(t *testing.T) {
	m := NewMatcher()
	if !MatchAny(m, []string{"app.exe"}, "app.exe") {
		t.Error("single pattern match")
	}
	if MatchAny(m, []string{"app.exe"}, "other.exe") {
		t.Error("single pattern no match")
	}
}

// ─── Engine: нормализация паттерна при AddRule ─────────────────────────────

// TestEngine_AddRule_NormalizesPattern проверяет что AddRule нормализует
// паттерн (ToSlash + ToLower) так что Match корректно работает с любым регистром.
func TestEngine_AddRule_NormalizesPattern(t *testing.T) {
	e := NewEngine()
	// Добавляем с uppercase
	r, err := e.AddRule(Rule{Pattern: "CHROME.EXE", Action: ActionProxy, Enabled: true})
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	// Паттерн должен быть нормализован
	if r.Pattern != "chrome.exe" {
		t.Errorf("Pattern после AddRule = %q, want chrome.exe", r.Pattern)
	}
	// Match должен работать
	if !e.Match("chrome.exe").Matched {
		t.Error("Match с нормализованным паттерном должен работать")
	}
}

func TestEngine_UpdateRule_NormalizesPattern(t *testing.T) {
	e := NewEngine()
	added, _ := e.AddRule(Rule{Pattern: "old.exe", Action: ActionProxy, Enabled: true})

	updated, err := e.UpdateRule(added.ID, Rule{
		Pattern: "NEW_APP.EXE",
		Action:  ActionDirect,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("UpdateRule: %v", err)
	}
	if updated.Pattern != "new_app.exe" {
		t.Errorf("Pattern после UpdateRule = %q, want new_app.exe", updated.Pattern)
	}
}

// ─── Engine: TimestampFields ──────────────────────────────────────────────

func TestEngine_AddRule_SetsCreatedAtAndUpdatedAt(t *testing.T) {
	e := NewEngine()
	r, _ := e.AddRule(Rule{Pattern: "app.exe", Action: ActionProxy, Enabled: true})
	if r.CreatedAt.IsZero() {
		t.Error("CreatedAt не должен быть нулевым")
	}
	if r.UpdatedAt.IsZero() {
		t.Error("UpdatedAt не должен быть нулевым")
	}
	if !r.CreatedAt.Equal(r.UpdatedAt) {
		t.Error("CreatedAt и UpdatedAt должны быть равны сразу после AddRule")
	}
}

// ─── Engine: FindMatchingRule как алиас Match ─────────────────────────────

func TestEngine_FindMatchingRule_SameBehaviorAsMatch(t *testing.T) {
	e := NewEngine()
	e.AddRule(Rule{Pattern: "chrome.exe", Action: ActionProxy, Priority: 10, Enabled: true})

	m1 := e.Match("chrome.exe")
	m2 := e.FindMatchingRule("chrome.exe")

	if m1.Matched != m2.Matched {
		t.Errorf("FindMatchingRule и Match дают разные Matched: %v vs %v", m1.Matched, m2.Matched)
	}
	if m1.Matched && m2.Matched && m1.Rule.Action != m2.Rule.Action {
		t.Errorf("FindMatchingRule и Match дают разные Action: %v vs %v", m1.Rule.Action, m2.Rule.Action)
	}
}

func TestEngine_FindMatchingRule_NoMatch_NilRule(t *testing.T) {
	e := NewEngine()
	m := e.FindMatchingRule("unknown.exe")
	if m.Matched {
		t.Error("не должно совпасть для неизвестного процесса")
	}
	if m.Rule != nil {
		t.Error("Rule должен быть nil при отсутствии совпадения")
	}
}

// ─── Engine: пустой список — ListRules ────────────────────────────────────

func TestEngine_ListRules_Empty(t *testing.T) {
	e := NewEngine()
	rules := e.ListRules()
	if rules == nil {
		t.Error("ListRules не должен возвращать nil")
	}
	if len(rules) != 0 {
		t.Errorf("ListRules на пустом engine = %d, want 0", len(rules))
	}
}

// ─── Engine: DisableRule для несуществующего ID ───────────────────────────

func TestEngine_DisableRule_MissingID_ReturnsError(t *testing.T) {
	e := NewEngine()
	if err := e.DisableRule("no-such-id"); err == nil {
		t.Error("DisableRule с несуществующим ID должен вернуть ошибку")
	}
}

// ─── Engine: UpdateRule для несуществующего ID ───────────────────────────

func TestEngine_UpdateRule_MissingID_ReturnsError(t *testing.T) {
	e := NewEngine()
	_, err := e.UpdateRule("no-such-id", Rule{Pattern: "app.exe", Action: ActionProxy, Enabled: true})
	if err == nil {
		t.Error("UpdateRule с несуществующим ID должен вернуть ошибку")
	}
}

// ─── Engine: Match возвращает копию правила ───────────────────────────────

// TestEngine_Match_ReturnsCopy гарантирует что мутация результата Match
// не влияет на внутреннее состояние engine.
func TestEngine_Match_ReturnsCopy(t *testing.T) {
	e := NewEngine()
	e.AddRule(Rule{Pattern: "app.exe", Action: ActionProxy, Priority: 5, Enabled: true})

	m := e.Match("app.exe")
	if !m.Matched {
		t.Fatal("должно совпасть")
	}
	// Мутируем возвращённое правило
	m.Rule.Action = ActionBlock

	// Второй Match должен вернуть оригинальный Action
	m2 := e.Match("app.exe")
	if m2.Rule.Action != ActionProxy {
		t.Errorf("мутация результата Match повлияла на engine: Action = %q", m2.Rule.Action)
	}
}

// ─── validateRule ─────────────────────────────────────────────────────────

func TestValidateRule_EmptyPattern(t *testing.T) {
	if err := validateRule(Rule{Pattern: "", Action: ActionProxy}); err == nil {
		t.Error("validateRule с пустым pattern должен вернуть ошибку")
	}
}

func TestValidateRule_InvalidAction(t *testing.T) {
	if err := validateRule(Rule{Pattern: "app.exe", Action: "INVALID"}); err == nil {
		t.Error("validateRule с невалидным action должен вернуть ошибку")
	}
}

func TestValidateRule_ValidRules(t *testing.T) {
	valid := []Rule{
		{Pattern: "app.exe", Action: ActionProxy},
		{Pattern: "*.exe", Action: ActionDirect},
		{Pattern: "blocker", Action: ActionBlock},
	}
	for _, r := range valid {
		if err := validateRule(r); err != nil {
			t.Errorf("validateRule(%+v) неожиданная ошибка: %v", r, err)
		}
	}
}
