package apprules

import (
	"sync"
	"testing"
)

// ═══════════════════════════════════════════════════════════════
// Matcher
// ═══════════════════════════════════════════════════════════════

func TestMatcher_ExactMatch(t *testing.T) {
	m := NewMatcher()
	cases := []struct {
		pattern, value string
		want           bool
	}{
		{"chrome.exe", "chrome.exe", true},
		{"chrome.exe", "firefox.exe", false},
		{"chrome.exe", "CHROME.EXE", true}, // регистронезависимо
	}
	for _, tc := range cases {
		got := m.Match(tc.pattern, tc.value)
		if got != tc.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tc.pattern, tc.value, got, tc.want)
		}
	}
}

func TestMatcher_WildcardExe(t *testing.T) {
	m := NewMatcher()
	if !m.Match("*.exe", "chrome.exe") {
		t.Error("*.exe должен совпадать с chrome.exe")
	}
	if !m.Match("*.exe", "firefox.exe") {
		t.Error("*.exe должен совпадать с firefox.exe")
	}
	if m.Match("*.exe", "notanexe") {
		t.Error("*.exe не должен совпадать с notanexe")
	}
}

func TestMatcher_FullPath_MatchesByBasename(t *testing.T) {
	m := NewMatcher()
	// Паттерн "chrome.exe" должен совпадать с полным путём
	if !m.Match("chrome.exe", "C:\\Program Files\\Google\\Chrome\\chrome.exe") {
		t.Error("chrome.exe должен совпадать по basename полного пути")
	}
}

func TestMatcher_PartialContains_BasenameOnly(t *testing.T) {
	m := NewMatcher()
	// "chrome" содержится в "chrome.exe" — basename-contains
	if !m.Match("chrome", "chrome.exe") {
		t.Error("«chrome» должен находить «chrome.exe» через basename contains")
	}
	// "chrome" НЕ должен совпадать с путём /path/to/chromium.exe только потому что
	// путь содержит "chrome" — сравниваем только basename
	got := m.Match("notachrome", "chrome.exe")
	if got {
		t.Error("«notachrome» не должен совпадать с chrome.exe")
	}
}

func TestMatchAny(t *testing.T) {
	m := NewMatcher()
	patterns := []string{"chrome.exe", "firefox.exe", "edge.exe"}

	if !MatchAny(m, patterns, "firefox.exe") {
		t.Error("MatchAny должен вернуть true для firefox.exe")
	}
	if MatchAny(m, patterns, "opera.exe") {
		t.Error("MatchAny должен вернуть false для opera.exe")
	}
	if MatchAny(m, nil, "anything.exe") {
		t.Error("MatchAny с пустыми паттернами должен вернуть false")
	}
}

// ═══════════════════════════════════════════════════════════════
// Engine — CRUD
// ═══════════════════════════════════════════════════════════════

func newTestRule(pattern string, action Action, priority int) Rule {
	return Rule{
		Pattern:  pattern,
		Action:   action,
		Priority: priority,
		Enabled:  true,
	}
}

func TestEngine_AddRule_AssignsID(t *testing.T) {
	e := NewEngine()
	r, err := e.AddRule(newTestRule("chrome.exe", ActionProxy, 10))
	if err != nil {
		t.Fatalf("AddRule failed: %v", err)
	}
	if r.ID == "" {
		t.Error("AddRule должен назначать непустой ID")
	}
}

func TestEngine_AddRule_EmptyPattern_ReturnsError(t *testing.T) {
	e := NewEngine()
	_, err := e.AddRule(Rule{Pattern: "", Action: ActionProxy})
	if err == nil {
		t.Error("AddRule с пустым Pattern должен вернуть ошибку")
	}
}

func TestEngine_AddRule_InvalidAction_ReturnsError(t *testing.T) {
	e := NewEngine()
	_, err := e.AddRule(Rule{Pattern: "app.exe", Action: "INVALID"})
	if err == nil {
		t.Error("AddRule с невалидным Action должен вернуть ошибку")
	}
}

func TestEngine_GetRule_ReturnsAdded(t *testing.T) {
	e := NewEngine()
	added, _ := e.AddRule(newTestRule("app.exe", ActionDirect, 5))

	got, err := e.GetRule(added.ID)
	if err != nil {
		t.Fatalf("GetRule failed: %v", err)
	}
	if got.Pattern != "app.exe" {
		t.Errorf("Pattern = %q, want app.exe", got.Pattern)
	}
}

func TestEngine_GetRule_MissingID_ReturnsError(t *testing.T) {
	e := NewEngine()
	_, err := e.GetRule("nonexistent-id")
	if err == nil {
		t.Error("GetRule с несуществующим ID должен вернуть ошибку")
	}
}

func TestEngine_DeleteRule(t *testing.T) {
	e := NewEngine()
	r, _ := e.AddRule(newTestRule("app.exe", ActionProxy, 1))

	if err := e.DeleteRule(r.ID); err != nil {
		t.Fatalf("DeleteRule failed: %v", err)
	}
	if _, err := e.GetRule(r.ID); err == nil {
		t.Error("правило должно быть удалено")
	}
}

func TestEngine_DeleteRule_MissingID_ReturnsError(t *testing.T) {
	e := NewEngine()
	if err := e.DeleteRule("nonexistent"); err == nil {
		t.Error("DeleteRule с несуществующим ID должен вернуть ошибку")
	}
}

func TestEngine_UpdateRule(t *testing.T) {
	e := NewEngine()
	r, _ := e.AddRule(newTestRule("old.exe", ActionProxy, 1))

	updated, err := e.UpdateRule(r.ID, Rule{
		Pattern:  "new.exe",
		Action:   ActionDirect,
		Priority: 99,
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("UpdateRule failed: %v", err)
	}
	if updated.Pattern != "new.exe" {
		t.Errorf("Pattern = %q, want new.exe", updated.Pattern)
	}
	if updated.Action != ActionDirect {
		t.Errorf("Action = %q, want DIRECT", updated.Action)
	}
	if updated.ID != r.ID {
		t.Error("ID не должен меняться при UpdateRule")
	}
}

func TestEngine_ListRules_SortedByPriorityDesc(t *testing.T) {
	e := NewEngine()
	e.AddRule(newTestRule("low.exe", ActionProxy, 1))
	e.AddRule(newTestRule("high.exe", ActionProxy, 100))
	e.AddRule(newTestRule("mid.exe", ActionProxy, 50))

	rules := e.ListRules()
	if len(rules) != 3 {
		t.Fatalf("len = %d, want 3", len(rules))
	}
	if rules[0].Priority < rules[1].Priority || rules[1].Priority < rules[2].Priority {
		t.Error("правила должны быть отсортированы по убыванию приоритета")
	}
}

// ═══════════════════════════════════════════════════════════════
// Engine — Match / Enable / Disable
// ═══════════════════════════════════════════════════════════════

func TestEngine_Match_ExactName(t *testing.T) {
	e := NewEngine()
	e.AddRule(newTestRule("chrome.exe", ActionProxy, 10))

	m := e.Match("chrome.exe")
	if !m.Matched {
		t.Fatal("должно совпасть")
	}
	if m.Rule.Action != ActionProxy {
		t.Errorf("Action = %q, want PROXY", m.Rule.Action)
	}
}

func TestEngine_Match_NoMatch(t *testing.T) {
	e := NewEngine()
	e.AddRule(newTestRule("chrome.exe", ActionProxy, 10))

	m := e.Match("firefox.exe")
	if m.Matched {
		t.Error("firefox.exe не должен совпасть с паттерном chrome.exe")
	}
	if m.Rule != nil {
		t.Error("Rule должен быть nil при отсутствии совпадения")
	}
}

func TestEngine_Match_DisabledRuleSkipped(t *testing.T) {
	e := NewEngine()
	r, _ := e.AddRule(newTestRule("chrome.exe", ActionProxy, 10))
	e.DisableRule(r.ID)

	m := e.Match("chrome.exe")
	if m.Matched {
		t.Error("отключённое правило не должно совпадать")
	}
}

func TestEngine_Match_HigherPriorityWins(t *testing.T) {
	e := NewEngine()
	e.AddRule(newTestRule("chrome.exe", ActionDirect, 1))  // низкий приоритет
	e.AddRule(newTestRule("chrome.exe", ActionProxy, 100)) // высокий приоритет

	m := e.Match("chrome.exe")
	if !m.Matched {
		t.Fatal("должно совпасть")
	}
	if m.Rule.Action != ActionProxy {
		t.Errorf("Action = %q, want PROXY (высокий приоритет побеждает)", m.Rule.Action)
	}
}

func TestEngine_EnableDisable_Roundtrip(t *testing.T) {
	e := NewEngine()
	r, _ := e.AddRule(newTestRule("app.exe", ActionBlock, 5))

	// Disable
	if err := e.DisableRule(r.ID); err != nil {
		t.Fatalf("DisableRule: %v", err)
	}
	if e.Match("app.exe").Matched {
		t.Error("после Disable не должно совпадать")
	}

	// Enable
	if err := e.EnableRule(r.ID); err != nil {
		t.Fatalf("EnableRule: %v", err)
	}
	if !e.Match("app.exe").Matched {
		t.Error("после Enable должно совпадать")
	}
}

func TestEngine_EnableRule_MissingID_ReturnsError(t *testing.T) {
	e := NewEngine()
	if err := e.EnableRule("no-such-id"); err == nil {
		t.Error("EnableRule с несуществующим ID должен вернуть ошибку")
	}
}

// ═══════════════════════════════════════════════════════════════
// Engine — параллельная безопасность (race detector)
// ═══════════════════════════════════════════════════════════════

func TestEngine_ConcurrentAddMatchDelete(t *testing.T) {
	e := NewEngine()
	var wg sync.WaitGroup

	// Параллельно добавляем и матчим
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			r, _ := e.AddRule(Rule{
				Pattern: "app.exe",
				Action:  ActionProxy,
				Enabled: true,
			})
			if r != nil {
				e.Match("app.exe")
				e.DeleteRule(r.ID)
			}
		}(i)
	}

	// Параллельно читаем список
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.ListRules()
		}()
	}

	wg.Wait()
}

// ═══════════════════════════════════════════════════════════════
// Action.IsValid
// ═══════════════════════════════════════════════════════════════

func TestAction_IsValid(t *testing.T) {
	validActions := []Action{ActionDirect, ActionProxy, ActionBlock}
	for _, a := range validActions {
		if !a.IsValid() {
			t.Errorf("Action(%q).IsValid() = false, want true", a)
		}
	}

	invalidActions := []Action{"", "proxy", "ALLOW", "REJECT", "invalid"}
	for _, a := range invalidActions {
		if a.IsValid() {
			t.Errorf("Action(%q).IsValid() = true, want false", a)
		}
	}
}
