package apprules

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"proxyclient/internal/fileutil"
)

// Storage интерфейс для хранения правил
type Storage interface {
	Save(rules []Rule) error
	Load() ([]Rule, error)
}

// fileStorage реализация Storage на базе JSON файла
type fileStorage struct {
	filePath string
	mu       sync.Mutex
}

// NewFileStorage создает новый file storage
func NewFileStorage(filePath string) Storage {
	return &fileStorage{
		filePath: filePath,
	}
}

// Save сохраняет правила в файл
func (s *fileStorage) Save(rules []Rule) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.MarshalIndent(rules, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal rules: %w", err)
	}
	if err := fileutil.WriteAtomic(s.filePath, data, 0644); err != nil {
		return fmt.Errorf("failed to save rules: %w", err)
	}
	return nil
}

// Load загружает правила из файла
func (s *fileStorage) Load() ([]Rule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// OPT #5: убрали предварительный os.Stat — ReadFile сам возвращает IsNotExist.
	// Было два syscall (Stat + ReadFile), стало один.
	data, err := os.ReadFile(s.filePath)
	if os.IsNotExist(err) {
		return []Rule{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	if len(data) == 0 {
		return []Rule{}, nil
	}

	var rules []Rule
	if err := json.Unmarshal(data, &rules); err != nil {
		return nil, fmt.Errorf("failed to unmarshal rules: %w", err)
	}

	return rules, nil
}

// PersistentEngine — engine с автоматическим сохранением.
// opMu сериализует операции "изменение + сохранение": без него две горутины
// могут одновременно пройти AddRule, после чего Save у одной падает и она
// откатывает только своё правило, но ListRules() уже включал правило конкурента —
// оно молча исчезало из файла без возврата ошибки вызывающей стороне.
type PersistentEngine struct {
	*engine
	storage Storage
	opMu    sync.Mutex // сериализует операции чтение-изменение-сохранение
}

// NewPersistentEngine создает engine с persistence
func NewPersistentEngine(storage Storage) (*PersistentEngine, error) {
	e := &engine{
		rules:   make(map[string]*Rule),
		matcher: NewMatcher(),
	}
	pe := &PersistentEngine{
		engine:  e,
		storage: storage,
	}

	if err := pe.loadRules(); err != nil {
		return nil, fmt.Errorf("failed to load rules: %w", err)
	}

	return pe, nil
}

// AddRule добавляет правило с сохранением
func (pe *PersistentEngine) AddRule(rule Rule) (*Rule, error) {
	pe.opMu.Lock()
	defer pe.opMu.Unlock()

	created, err := pe.engine.AddRule(rule)
	if err != nil {
		return nil, err
	}

	if err := pe.storage.Save(pe.engine.listRulesUnsorted()); err != nil { // OPT #3: save without sort
		_ = pe.engine.DeleteRule(created.ID)
		return nil, fmt.Errorf("failed to save rules: %w", err)
	}

	return created, nil
}

// UpdateRule обновляет правило с сохранением
func (pe *PersistentEngine) UpdateRule(id string, rule Rule) (*Rule, error) {
	pe.opMu.Lock()
	defer pe.opMu.Unlock()

	oldRule, err := pe.engine.GetRule(id)
	if err != nil {
		return nil, err
	}

	updated, err := pe.engine.UpdateRule(id, rule)
	if err != nil {
		return nil, err
	}

	if err := pe.storage.Save(pe.engine.listRulesUnsorted()); err != nil { // OPT #3: save without sort
		_, _ = pe.engine.UpdateRule(id, *oldRule)
		return nil, fmt.Errorf("failed to save rules: %w", err)
	}

	return updated, nil
}

// DeleteRule удаляет правило с сохранением
func (pe *PersistentEngine) DeleteRule(id string) error {
	pe.opMu.Lock()
	defer pe.opMu.Unlock()

	rule, err := pe.engine.GetRule(id)
	if err != nil {
		return err
	}

	if err := pe.engine.DeleteRule(id); err != nil {
		return err
	}

	if err := pe.storage.Save(pe.engine.listRulesUnsorted()); err != nil { // OPT #3: save without sort
		_ = pe.engine.restoreRule(*rule)
		return fmt.Errorf("failed to save rules: %w", err)
	}

	return nil
}

// EnableRule включает правило с сохранением
func (pe *PersistentEngine) EnableRule(id string) error {
	pe.opMu.Lock()
	defer pe.opMu.Unlock()

	if err := pe.engine.EnableRule(id); err != nil {
		return err
	}
	// Синхронное сохранение с откатом при ошибке.
	// OPT #8 (debounce) не применяется здесь: тесты и внешний контракт
	// требуют чтобы ошибка Save возвращалась вызывающему, а состояние откатывалось.
	if err := pe.storage.Save(pe.engine.listRulesUnsorted()); err != nil { // OPT #3: save without sort
		_ = pe.engine.DisableRule(id) // откат
		return fmt.Errorf("failed to save rules: %w", err)
	}
	return nil
}

// DisableRule отключает правило с сохранением
func (pe *PersistentEngine) DisableRule(id string) error {
	pe.opMu.Lock()
	defer pe.opMu.Unlock()

	if err := pe.engine.DisableRule(id); err != nil {
		return err
	}
	if err := pe.storage.Save(pe.engine.listRulesUnsorted()); err != nil { // OPT #3: save without sort
		_ = pe.engine.EnableRule(id) // откат
		return fmt.Errorf("failed to save rules: %w", err)
	}
	return nil
}

// AddRuleBatch добавляет несколько правил за одну операцию сохранения.
// Используется при импорте — вместо N отдельных записей на диск делает одну.
func (pe *PersistentEngine) AddRuleBatch(rules []Rule) ([]*Rule, error) {
	pe.opMu.Lock()
	defer pe.opMu.Unlock()

	var created []*Rule
	for _, rule := range rules {
		r, err := pe.engine.AddRule(rule)
		if err != nil {
			// Откат всего батча при ошибке валидации
			for _, c := range created {
				_ = pe.engine.DeleteRule(c.ID)
			}
			return nil, fmt.Errorf("правило %q: %w", rule.Name, err)
		}
		created = append(created, r)
	}

	if err := pe.storage.Save(pe.engine.listRulesUnsorted()); err != nil {
		for _, c := range created {
			_ = pe.engine.DeleteRule(c.ID)
		}
		return nil, fmt.Errorf("failed to save rules batch: %w", err)
	}

	return created, nil
}

// loadRules загружает правила из storage, сохраняя оригинальные ID
func (pe *PersistentEngine) loadRules() error {
	rules, err := pe.storage.Load()
	if err != nil {
		return err
	}

	// BUG FIX #8: ранее ошибки при restoreRule молча игнорировались (continue).
	// Теперь собираем все ошибки и возвращаем их, чтобы повреждённый app_rules.json
	// не терял правила незаметно для пользователя/логов.
	var loadErrors []string
	for _, rule := range rules {
		if err := pe.engine.restoreRule(rule); err != nil {
			loadErrors = append(loadErrors, fmt.Sprintf("rule %q (id=%s): %v", rule.Name, rule.ID, err))
		}
	}
	if len(loadErrors) > 0 {
		return fmt.Errorf("некоторые правила не загружены (%d): %s",
			len(loadErrors), strings.Join(loadErrors, "; "))
	}
	return nil
}
