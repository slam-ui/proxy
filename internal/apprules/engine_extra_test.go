package apprules

import (
	"sync"
	"testing"
)

// ── UpdateRule: сохраняет CreatedAt ──────────────────────────────────────

// BUG-РИСК: UpdateRule должен сохранять CreatedAt оригинального правила.
// Если нарушен — UI покажет неверную дату создания.
func TestEngine_UpdateRule_PreservesCreatedAt(t *testing.T) {
	e := NewEngine()
	r, _ := e.AddRule(newTestRule("app.exe", ActionProxy, 10))
	createdAt := r.CreatedAt

	updated, err := e.UpdateRule(r.ID, Rule{
		Pattern:  "new_app.exe",
		Action:   ActionDirect,
		Priority: 5,
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("UpdateRule failed: %v", err)
	}
	if !updated.CreatedAt.Equal(createdAt) {
		t.Errorf("UpdateRule изменил CreatedAt: %v → %v", createdAt, updated.CreatedAt)
	}
}

// ── Match: задизейбленные правила не матчатся ─────────────────────────────

// BUG-РИСК: Match должен использовать sorted кэш, который включает
// только Enabled=true. Задизейбленное правило НЕ должно матчиться.
func TestEngine_Match_DisabledRule_NotMatched(t *testing.T) {
	e := NewEngine()
	r, _ := e.AddRule(Rule{Pattern: "blocked_app.exe", Action: ActionBlock, Priority: 100, Enabled: true})
	e.DisableRule(r.ID)

	match := e.Match("blocked_app.exe")
	if match.Matched {
		t.Error("задизейбленное правило не должно матчиться")
	}
}

// BUG-РИСК: после EnableRule правило снова должно матчиться.
func TestEngine_EnableRule_RestoredToMatch(t *testing.T) {
	e := NewEngine()
	r, _ := e.AddRule(Rule{Pattern: "app.exe", Action: ActionProxy, Priority: 10, Enabled: true})
	e.DisableRule(r.ID)
	e.EnableRule(r.ID)

	match := e.Match("app.exe")
	if !match.Matched {
		t.Error("после EnableRule правило должно снова матчиться")
	}
}

// ── Match: приоритет — высокий выигрывает ─────────────────────────────────

// BUG-РИСК: wildcard *.exe с низким приоритетом не должен перебивать
// точное правило с высоким приоритетом.
func TestEngine_Match_Priority_HigherWins(t *testing.T) {
	e := NewEngine()
	e.AddRule(Rule{Pattern: "*.exe", Action: ActionDirect, Priority: 5, Enabled: true})
	e.AddRule(Rule{Pattern: "chrome.exe", Action: ActionProxy, Priority: 100, Enabled: true})

	match := e.Match("chrome.exe")
	if !match.Matched {
		t.Fatal("должен быть матч")
	}
	if match.Rule.Action != ActionProxy {
		t.Errorf("Action = %q, want PROXY (высокий приоритет должен побеждать)", match.Rule.Action)
	}
}

// ── ListRules: включает отключённые ──────────────────────────────────────

// BUG-РИСК: ListRules должен возвращать ВСЕ правила (Enabled И Disabled),
// а не только те, что в sorted кэше.
func TestEngine_ListRules_IncludesDisabled(t *testing.T) {
	e := NewEngine()
	e.AddRule(Rule{Pattern: "a.exe", Action: ActionProxy, Priority: 1, Enabled: true})
	r2, _ := e.AddRule(Rule{Pattern: "b.exe", Action: ActionDirect, Priority: 2, Enabled: true})
	e.DisableRule(r2.ID)

	rules := e.ListRules()
	if len(rules) != 2 {
		t.Errorf("ListRules len = %d, want 2 (включая отключённые)", len(rules))
	}
}

// BUG-РИСК: ListRules на пустом engine не должен возвращать nil.
func TestEngine_ListRules_Empty_NotNil(t *testing.T) {
	e := NewEngine()
	rules := e.ListRules()
	if rules == nil {
		t.Error("ListRules на пустом engine не должен возвращать nil")
	}
}

// ── DeleteRule: удалённое правило не матчится ─────────────────────────────

func TestEngine_DeleteRule_RemovedFromMatch(t *testing.T) {
	e := NewEngine()
	r, _ := e.AddRule(Rule{Pattern: "temp.exe", Action: ActionProxy, Priority: 10, Enabled: true})
	e.DeleteRule(r.ID)

	match := e.Match("temp.exe")
	if match.Matched {
		t.Error("удалённое правило не должно матчиться")
	}
}

// ── Concurrent safety ─────────────────────────────────────────────────────

// BUG-РИСК: конкурентный доступ к Match и мутациям — race detector
// не должен сработать. Запускать с -race флагом.
func TestEngine_Match_ConcurrentWithMutations(t *testing.T) {
	e := NewEngine()
	r, _ := e.AddRule(Rule{Pattern: "app.exe", Action: ActionProxy, Priority: 1, Enabled: true})

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.Match("app.exe")
		}()
	}
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			e.DisableRule(id)
			e.EnableRule(id)
		}(r.ID)
	}
	wg.Wait()
}

// ── UpdateRule: несуществующий ID ─────────────────────────────────────────

func TestEngine_UpdateRule_NonexistentID_ReturnsError(t *testing.T) {
	e := NewEngine()
	_, err := e.UpdateRule("nonexistent-id", Rule{Pattern: "app.exe", Action: ActionProxy, Enabled: true})
	if err == nil {
		t.Error("UpdateRule несуществующего ID должен вернуть ошибку")
	}
}

// ── EnableRule/DisableRule: несуществующий ID ─────────────────────────────

func TestEngine_EnableDisable_NonexistentID_ReturnsError(t *testing.T) {
	e := NewEngine()
	if err := e.EnableRule("ghost"); err == nil {
		t.Error("EnableRule несуществующего ID должен вернуть ошибку")
	}
	if err := e.DisableRule("ghost"); err == nil {
		t.Error("DisableRule несуществующего ID должен вернуть ошибку")
	}
}

// ── GetRule: возвращает копию, не указатель ───────────────────────────────

// BUG-РИСК: мутация возвращённого правила не должна влиять на внутреннее состояние.
func TestEngine_GetRule_ReturnsCopy(t *testing.T) {
	e := NewEngine()
	r, _ := e.AddRule(newTestRule("app.exe", ActionProxy, 10))

	got, _ := e.GetRule(r.ID)
	got.Pattern = "mutated.exe"

	// Оригинал не должен измениться
	original, _ := e.GetRule(r.ID)
	if original.Pattern == "mutated.exe" {
		t.Error("GetRule должен возвращать копию — мутация не должна влиять на engine")
	}
}
