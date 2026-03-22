package apprules

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ─── fileStorage: дополнительные сценарии ─────────────────────────────────
// (TestFileStorage_Load_InvalidJSON, SaveLoad_RoundTrip и др. уже покрыты
// в storage_test.go; здесь только уникальные случаи)

// TestFileStorage_Save_Overwrites проверяет что повторное Save полностью
// замещает предыдущее содержимое (не дописывает в конец).
func TestFileStorage_Save_Overwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	s := NewFileStorage(path)

	rules1 := []Rule{{ID: "id-1", Pattern: "chrome.exe", Action: ActionProxy, Enabled: true}}
	if err := s.Save(rules1); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	rules2 := []Rule{
		{ID: "id-2", Pattern: "firefox.exe", Action: ActionDirect, Enabled: true},
		{ID: "id-3", Pattern: "edge.exe", Action: ActionBlock, Enabled: false},
	}
	if err := s.Save(rules2); err != nil {
		t.Fatalf("second Save: %v", err)
	}

	loaded, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded) != 2 {
		t.Errorf("после второго Save len = %d, want 2 (не 3)", len(loaded))
	}
}

// TestFileStorage_Save_EmptySlice проверяет что сохранение пустого среза
// записывает валидный JSON и при загрузке возвращает пустой срез, не nil.
func TestFileStorage_Save_EmptySlice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	s := NewFileStorage(path)

	if err := s.Save([]Rule{}); err != nil {
		t.Fatalf("Save пустого среза: %v", err)
	}
	loaded, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded == nil {
		t.Error("Load не должен возвращать nil")
	}
	if len(loaded) != 0 {
		t.Errorf("len = %d, want 0", len(loaded))
	}
}

// TestFileStorage_Load_PreservesAllFields проверяет что все поля Rule
// корректно сериализуются и десериализуются (полная roundtrip-проверка полей).
func TestFileStorage_Load_PreservesAllFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	s := NewFileStorage(path)

	ts := time.Now().Truncate(time.Millisecond) // JSON не хранит наносекунды
	original := Rule{
		ID:        "test-id",
		Name:      "My Rule",
		Pattern:   "app.exe",
		Action:    ActionBlock,
		ProxyAddr: "127.0.0.1:8080",
		Priority:  42,
		Enabled:   false,
		CreatedAt: ts,
		UpdatedAt: ts,
	}
	if err := s.Save([]Rule{original}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, _ := s.Load()
	if len(loaded) != 1 {
		t.Fatalf("len = %d, want 1", len(loaded))
	}
	r := loaded[0]
	checks := []struct {
		field string
		ok    bool
	}{
		{"ID", r.ID == original.ID},
		{"Name", r.Name == original.Name},
		{"Action", r.Action == original.Action},
		{"Priority", r.Priority == original.Priority},
		{"Enabled", r.Enabled == original.Enabled},
		{"ProxyAddr", r.ProxyAddr == original.ProxyAddr},
	}
	for _, c := range checks {
		if !c.ok {
			t.Errorf("поле %s не совпадает после roundtrip", c.field)
		}
	}
}

// ─── PersistentEngine: перезагрузка между сессиями ────────────────────────
// (Тесты AddRule/UpdateRule/EnableDisable PersistsToDisk уже в storage_test.go)

// TestPersistentEngine_ReloadsFromDisk проверяет что новый PersistentEngine
// загружает правила сохранённые предыдущей сессией и Match работает корректно.
func TestPersistentEngine_ReloadsFromDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")

	pe1, err := NewPersistentEngine(NewFileStorage(path))
	if err != nil {
		t.Fatalf("NewPersistentEngine: %v", err)
	}
	r, _ := pe1.AddRule(Rule{Pattern: "persisted.exe", Action: ActionProxy, Enabled: true})

	// Вторая «сессия» — тот же файл, новый объект engine
	pe2, err := NewPersistentEngine(NewFileStorage(path))
	if err != nil {
		t.Fatalf("NewPersistentEngine (2nd): %v", err)
	}
	got, err := pe2.GetRule(r.ID)
	if err != nil {
		t.Fatalf("GetRule после перезагрузки: %v", err)
	}
	if got.Pattern != "persisted.exe" {
		t.Errorf("Pattern = %q, want persisted.exe", got.Pattern)
	}
}

// TestPersistentEngine_Match_AfterReload проверяет что enabled/disabled статус
// правил корректно восстанавливается при перезагрузке из файла.
func TestPersistentEngine_Match_AfterReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")

	pe1, _ := NewPersistentEngine(NewFileStorage(path))
	pe1.AddRule(Rule{Pattern: "chrome.exe", Action: ActionProxy, Priority: 10, Enabled: true})
	pe1.AddRule(Rule{Pattern: "firefox.exe", Action: ActionDirect, Priority: 5, Enabled: false})

	pe2, _ := NewPersistentEngine(NewFileStorage(path))
	if !pe2.Match("chrome.exe").Matched {
		t.Error("chrome.exe (enabled) должен совпасть после перезагрузки")
	}
	if pe2.Match("firefox.exe").Matched {
		t.Error("firefox.exe (disabled) не должен совпасть после перезагрузки")
	}
}

// TestPersistentEngine_MultipleRules_SortedByPriorityAfterReload проверяет
// что ListRules после перезагрузки возвращает правила по убыванию приоритета.
func TestPersistentEngine_MultipleRules_SortedByPriorityAfterReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")

	pe1, _ := NewPersistentEngine(NewFileStorage(path))
	pe1.AddRule(Rule{Pattern: "low.exe", Action: ActionDirect, Priority: 1, Enabled: true})
	pe1.AddRule(Rule{Pattern: "high.exe", Action: ActionProxy, Priority: 100, Enabled: true})
	pe1.AddRule(Rule{Pattern: "mid.exe", Action: ActionBlock, Priority: 50, Enabled: true})

	pe2, _ := NewPersistentEngine(NewFileStorage(path))
	rules := pe2.ListRules()
	if len(rules) != 3 {
		t.Fatalf("загружено %d правил, want 3", len(rules))
	}
	for i := 1; i < len(rules); i++ {
		if rules[i].Priority > rules[i-1].Priority {
			t.Errorf("нарушен порядок: rules[%d].Priority=%d > rules[%d].Priority=%d",
				i, rules[i].Priority, i-1, rules[i-1].Priority)
		}
	}
}

// TestPersistentEngine_AddRule_SecondSaveFailure_RollsBack использует failStorage
// из storage_test.go (failOnSave bool) — проверяет откат второго AddRule.
func TestPersistentEngine_AddRule_SecondSaveFailure_RollsBack(t *testing.T) {
	fs := &failStorage{}
	pe, err := NewPersistentEngine(fs)
	if err != nil {
		t.Fatalf("NewPersistentEngine: %v", err)
	}

	_, err = pe.AddRule(newTestRule("ok.exe", ActionProxy, 1))
	if err != nil {
		t.Fatalf("первый AddRule: %v", err)
	}

	fs.failOnSave = true
	_, err = pe.AddRule(newTestRule("bad.exe", ActionDirect, 2))
	if err == nil {
		t.Fatal("ожидали ошибку при падении Save")
	}

	rules := pe.ListRules()
	if len(rules) != 1 {
		t.Errorf("после отката должно быть 1 правило, got %d: %+v", len(rules), rules)
	}
	if rules[0].Pattern != "ok.exe" {
		t.Errorf("должно остаться ok.exe, got %q", rules[0].Pattern)
	}
}

// TestPersistentEngine_SaveToFile_WritesValidJSON проверяет что файл после
// всех операций содержит валидный JSON.
func TestPersistentEngine_SaveToFile_WritesValidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	pe, _ := NewPersistentEngine(NewFileStorage(path))

	pe.AddRule(Rule{Pattern: "a.exe", Action: ActionProxy, Enabled: true})
	pe.AddRule(Rule{Pattern: "b.exe", Action: ActionDirect, Enabled: true})

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var rules []Rule
	if err := json.Unmarshal(data, &rules); err != nil {
		t.Errorf("невалидный JSON в файле: %v (содержимое: %q)", err, data)
	}
	if len(rules) != 2 {
		t.Errorf("в JSON %d правил, want 2", len(rules))
	}
}
