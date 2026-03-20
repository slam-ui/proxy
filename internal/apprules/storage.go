package apprules

import (
	"encoding/json"
	"fmt"
	"os"
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

	if _, err := os.Stat(s.filePath); os.IsNotExist(err) {
		return []Rule{}, nil
	}

	data, err := os.ReadFile(s.filePath)
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

	if err := pe.storage.Save(pe.engine.ListRules()); err != nil {
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

	if err := pe.storage.Save(pe.engine.ListRules()); err != nil {
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

	if err := pe.storage.Save(pe.engine.ListRules()); err != nil {
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
	// BUG FIX: при ошибке сохранения откатываем изменение в памяти,
	// иначе in-memory состояние расходится с файлом до следующего рестарта.
	if err := pe.storage.Save(pe.engine.ListRules()); err != nil {
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
	// BUG FIX: при ошибке сохранения откатываем изменение в памяти,
	// иначе in-memory состояние расходится с файлом до следующего рестарта.
	if err := pe.storage.Save(pe.engine.ListRules()); err != nil {
		_ = pe.engine.EnableRule(id) // откат
		return fmt.Errorf("failed to save rules: %w", err)
	}
	return nil
}

// loadRules загружает правила из storage, сохраняя оригинальные ID
func (pe *PersistentEngine) loadRules() error {
	rules, err := pe.storage.Load()
	if err != nil {
		return err
	}

	for _, rule := range rules {
		if err := pe.engine.restoreRule(rule); err != nil {
			continue
		}
	}

	return nil
}
