package apprules

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

func newTestPE(t *testing.T) (*PersistentEngine, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rules.json")
	pe, err := NewPersistentEngine(NewFileStorage(path))
	if err != nil {
		t.Fatalf("NewPersistentEngine: %v", err)
	}
	return pe, path
}

func loadRulesFromDisk(t *testing.T, path string) []Rule {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var rules []Rule
	if err := json.Unmarshal(data, &rules); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	return rules
}

// ─── fileStorage ─────────────────────────────────────────────────────────────

func TestFileStorage_SaveLoad_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	s := NewFileStorage(path)

	rules := []Rule{
		{ID: "id-1", Pattern: "chrome.exe", Action: ActionProxy, Priority: 10, Enabled: true},
		{ID: "id-2", Pattern: "*.exe", Action: ActionDirect, Priority: 5, Enabled: false},
	}

	if err := s.Save(rules); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("len = %d, want 2", len(loaded))
	}
	if loaded[0].ID != "id-1" || loaded[1].ID != "id-2" {
		t.Errorf("IDs не совпадают: %+v", loaded)
	}
	if loaded[1].Enabled != false {
		t.Error("Enabled должен сохраняться как false")
	}
}

func TestFileStorage_Load_MissingFile_ReturnsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent.json")
	s := NewFileStorage(path)
	rules, err := s.Load()
	if err != nil {
		t.Fatalf("Load не должен возвращать ошибку для несуществующего файла: %v", err)
	}
	if len(rules) != 0 {
		t.Errorf("len = %d, want 0", len(rules))
	}
}

func TestFileStorage_Load_EmptyFile_ReturnsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	os.WriteFile(path, []byte{}, 0644)
	s := NewFileStorage(path)
	rules, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(rules) != 0 {
		t.Errorf("len = %d, want 0", len(rules))
	}
}

func TestFileStorage_Load_InvalidJSON_ReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	os.WriteFile(path, []byte("{not valid json"), 0644)
	s := NewFileStorage(path)
	_, err := s.Load()
	if err == nil {
		t.Fatal("ожидали ошибку для невалидного JSON")
	}
}

func TestFileStorage_Save_Atomic_NoTempFileLeft(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.json")
	s := NewFileStorage(path)

	if err := s.Save([]Rule{{ID: "x", Pattern: "a.exe", Action: ActionProxy}}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("временный файл остался: %s", e.Name())
		}
	}
}

// ─── PersistentEngine — базовые CRUD операции ────────────────────────────────

func TestPersistentEngine_NewEngine_EmptyFile_NoRules(t *testing.T) {
	pe, _ := newTestPE(t)
	if rules := pe.ListRules(); len(rules) != 0 {
		t.Errorf("ожидали пустой список, got %d правил", len(rules))
	}
}

func TestPersistentEngine_AddRule_PersistsToDisk(t *testing.T) {
	pe, path := newTestPE(t)
	r, err := pe.AddRule(newTestRule("chrome.exe", ActionProxy, 10))
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}

	disk := loadRulesFromDisk(t, path)
	if len(disk) != 1 {
		t.Fatalf("на диске %d правил, want 1", len(disk))
	}
	if disk[0].ID != r.ID {
		t.Errorf("ID на диске = %q, want %q", disk[0].ID, r.ID)
	}
	if disk[0].Pattern != "chrome.exe" {
		t.Errorf("Pattern = %q", disk[0].Pattern)
	}
}

func TestPersistentEngine_DeleteRule_RemovesFromDisk(t *testing.T) {
	pe, path := newTestPE(t)
	r, _ := pe.AddRule(newTestRule("app.exe", ActionDirect, 1))

	if err := pe.DeleteRule(r.ID); err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}

	disk := loadRulesFromDisk(t, path)
	if len(disk) != 0 {
		t.Errorf("на диске %d правил после удаления, want 0", len(disk))
	}
}

func TestPersistentEngine_UpdateRule_PersistsToDisk(t *testing.T) {
	pe, path := newTestPE(t)
	r, _ := pe.AddRule(newTestRule("old.exe", ActionProxy, 5))

	_, err := pe.UpdateRule(r.ID, Rule{Pattern: "new.exe", Action: ActionBlock, Priority: 99, Enabled: true})
	if err != nil {
		t.Fatalf("UpdateRule: %v", err)
	}

	disk := loadRulesFromDisk(t, path)
	if len(disk) != 1 || disk[0].Pattern != "new.exe" {
		t.Errorf("диск: %+v", disk)
	}
	if disk[0].Action != ActionBlock {
		t.Errorf("Action = %q, want BLOCK", disk[0].Action)
	}
}

func TestPersistentEngine_EnableDisable_PersistsToDisk(t *testing.T) {
	pe, path := newTestPE(t)
	r, _ := pe.AddRule(newTestRule("app.exe", ActionProxy, 1))

	// Disable
	if err := pe.DisableRule(r.ID); err != nil {
		t.Fatalf("DisableRule: %v", err)
	}
	disk := loadRulesFromDisk(t, path)
	if disk[0].Enabled != false {
		t.Error("Enabled должен быть false после Disable")
	}

	// Enable
	if err := pe.EnableRule(r.ID); err != nil {
		t.Fatalf("EnableRule: %v", err)
	}
	disk = loadRulesFromDisk(t, path)
	if disk[0].Enabled != true {
		t.Error("Enabled должен быть true после Enable")
	}
}

// ─── PersistentEngine — загрузка с диска ─────────────────────────────────────

func TestPersistentEngine_LoadExistingRules_PreservesIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	s := NewFileStorage(path)

	// Сохраняем правила с конкретными ID напрямую через storage
	original := []Rule{
		{ID: "stable-id-1", Pattern: "chrome.exe", Action: ActionProxy, Priority: 10, Enabled: true},
		{ID: "stable-id-2", Pattern: "firefox.exe", Action: ActionDirect, Priority: 5, Enabled: true},
	}
	s.Save(original)

	// Создаём новый engine — он должен загрузить сохранённые правила
	pe, err := NewPersistentEngine(s)
	if err != nil {
		t.Fatalf("NewPersistentEngine: %v", err)
	}

	rules := pe.ListRules()
	if len(rules) != 2 {
		t.Fatalf("загружено %d правил, want 2", len(rules))
	}

	ids := map[string]bool{}
	for _, r := range rules {
		ids[r.ID] = true
	}
	if !ids["stable-id-1"] || !ids["stable-id-2"] {
		t.Errorf("ID не совпадают: %+v", rules)
	}
}

func TestPersistentEngine_LoadedRules_CanMatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	s := NewFileStorage(path)

	s.Save([]Rule{
		{ID: "r1", Pattern: "chrome.exe", Action: ActionProxy, Priority: 10, Enabled: true},
	})

	pe, _ := NewPersistentEngine(s)
	m := pe.Match("chrome.exe")
	if !m.Matched {
		t.Fatal("загруженное правило должно матчиться")
	}
	if m.Rule.Action != ActionProxy {
		t.Errorf("Action = %q, want PROXY", m.Rule.Action)
	}
}

// ─── PersistentEngine — откат при ошибке сохранения ─────────────────────────

// failStorage — хранилище, которое намеренно падает при Save
type failStorage struct {
	failOnSave bool
	data       []Rule
}

func (f *failStorage) Save(rules []Rule) error {
	if f.failOnSave {
		return os.ErrPermission
	}
	f.data = append([]Rule(nil), rules...)
	return nil
}

func (f *failStorage) Load() ([]Rule, error) {
	return append([]Rule(nil), f.data...), nil
}

func TestPersistentEngine_AddRule_SaveFailure_RollsBack(t *testing.T) {
	fs := &failStorage{}
	pe, err := NewPersistentEngine(fs)
	if err != nil {
		t.Fatalf("NewPersistentEngine: %v", err)
	}

	// Первое правило — успешно
	_, err = pe.AddRule(newTestRule("ok.exe", ActionProxy, 1))
	if err != nil {
		t.Fatalf("первый AddRule: %v", err)
	}

	// Следующие попытки — Save падает
	fs.failOnSave = true
	_, err = pe.AddRule(newTestRule("fail.exe", ActionProxy, 2))
	if err == nil {
		t.Fatal("ожидали ошибку при падении Save")
	}

	// Откат: неудавшееся правило не должно остаться в памяти
	rules := pe.ListRules()
	if len(rules) != 1 {
		t.Errorf("после отката должно быть 1 правило, got %d: %+v", len(rules), rules)
	}
	if rules[0].Pattern != "ok.exe" {
		t.Errorf("должно остаться ok.exe, got %q", rules[0].Pattern)
	}
}

func TestPersistentEngine_DeleteRule_SaveFailure_RollsBack(t *testing.T) {
	fs := &failStorage{}
	pe, err := NewPersistentEngine(fs)
	if err != nil {
		t.Fatalf("NewPersistentEngine: %v", err)
	}

	r, _ := pe.AddRule(newTestRule("app.exe", ActionProxy, 1))

	fs.failOnSave = true
	if err := pe.DeleteRule(r.ID); err == nil {
		t.Fatal("ожидали ошибку при падении Save")
	}

	// Правило должно остаться в памяти (откат удаления)
	if _, err := pe.GetRule(r.ID); err != nil {
		t.Errorf("правило должно быть восстановлено после отката удаления: %v", err)
	}
}

func TestPersistentEngine_EnableRule_SaveFailure_RollsBack(t *testing.T) {
	fs := &failStorage{}
	pe, _ := NewPersistentEngine(fs)

	r, _ := pe.AddRule(Rule{Pattern: "app.exe", Action: ActionProxy, Enabled: true})
	pe.DisableRule(r.ID) // вначале выключаем успешно

	fs.failOnSave = true
	if err := pe.EnableRule(r.ID); err == nil {
		t.Fatal("ожидали ошибку при падении Save")
	}

	// Правило должно остаться disabled (откат enable)
	got, _ := pe.GetRule(r.ID)
	if got.Enabled {
		t.Error("правило должно остаться disabled после отката EnableRule")
	}
}

func TestPersistentEngine_DisableRule_SaveFailure_RollsBack(t *testing.T) {
	fs := &failStorage{}
	pe, _ := NewPersistentEngine(fs)

	r, _ := pe.AddRule(newTestRule("app.exe", ActionProxy, 1)) // Enabled: true

	fs.failOnSave = true
	if err := pe.DisableRule(r.ID); err == nil {
		t.Fatal("ожидали ошибку при падении Save")
	}

	// Правило должно остаться enabled (откат disable)
	got, _ := pe.GetRule(r.ID)
	if !got.Enabled {
		t.Error("правило должно остаться enabled после отката DisableRule")
	}
}

// ─── PersistentEngine — UpdateRule сохраняет CreatedAt ───────────────────────

func TestPersistentEngine_UpdateRule_PreservesCreatedAt(t *testing.T) {
	pe, _ := newTestPE(t)
	r, _ := pe.AddRule(newTestRule("old.exe", ActionProxy, 1))
	originalCreatedAt := r.CreatedAt

	// Гарантируем что time.Now() в UpdateRule вернёт значение строго позже CreatedAt.
	// Без паузы AddRule и UpdateRule могут получить одинаковый timestamp
	// (os.timer resolution на Windows ~15ms), и After() вернёт false.
	time.Sleep(20 * time.Millisecond)

	updated, err := pe.UpdateRule(r.ID, Rule{Pattern: "new.exe", Action: ActionDirect, Priority: 99, Enabled: true})
	if err != nil {
		t.Fatalf("UpdateRule: %v", err)
	}

	if !updated.CreatedAt.Equal(originalCreatedAt) {
		t.Errorf("CreatedAt изменился: было %v, стало %v", originalCreatedAt, updated.CreatedAt)
	}
	if !updated.UpdatedAt.After(originalCreatedAt) {
		t.Errorf("UpdatedAt должен быть позже CreatedAt: UpdatedAt=%v, CreatedAt=%v",
			updated.UpdatedAt, originalCreatedAt)
	}
}

// ─── PersistentEngine — параллельная безопасность ────────────────────────────

func TestPersistentEngine_ConcurrentOps_NoRace(t *testing.T) {
	pe, _ := newTestPE(t)
	var wg sync.WaitGroup

	// Параллельно добавляем и удаляем
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := pe.AddRule(newTestRule("app.exe", ActionProxy, 1))
			if err == nil {
				pe.DeleteRule(r.ID)
			}
		}()
	}
	// Параллельно читаем
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pe.ListRules()
			pe.Match("app.exe")
		}()
	}
	wg.Wait()
}

// ─── fileStorage — потокобезопасность ────────────────────────────────────────

func TestFileStorage_ConcurrentSave_NoCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	s := NewFileStorage(path)
	rules := []Rule{{ID: "r1", Pattern: "app.exe", Action: ActionProxy, Enabled: true}}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Save(rules)
		}()
	}
	wg.Wait()

	loaded, err := s.Load()
	if err != nil {
		t.Fatalf("Load после concurrent Save: %v", err)
	}
	if len(loaded) != 1 {
		t.Errorf("len = %d, want 1", len(loaded))
	}
}
