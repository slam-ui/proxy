package apprules

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
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

	// Сериализуем в JSON
	data, err := json.MarshalIndent(rules, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal rules: %w", err)
	}

	// Сохраняем в файл
	if err := os.WriteFile(s.filePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	return nil
}

// Load загружает правила из файла
func (s *fileStorage) Load() ([]Rule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Проверяем существование файла
	if _, err := os.Stat(s.filePath); os.IsNotExist(err) {
		return []Rule{}, nil // Файл не существует - возвращаем пустой список
	}

	// Читаем файл
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	// Если файл пустой, возвращаем пустой список
	if len(data) == 0 {
		return []Rule{}, nil
	}

	// Десериализуем
	var rules []Rule
	if err := json.Unmarshal(data, &rules); err != nil {
		return nil, fmt.Errorf("failed to unmarshal rules: %w", err)
	}

	return rules, nil
}

// PersistentEngine engine с автоматическим сохранением
type PersistentEngine struct {
	Engine
	storage Storage
	mu      sync.Mutex
}

// NewPersistentEngine создает engine с persistence
func NewPersistentEngine(storage Storage) (*PersistentEngine, error) {
	engine := NewEngine()
	pe := &PersistentEngine{
		Engine:  engine,
		storage: storage,
	}

	// Загружаем существующие правила
	if err := pe.loadRules(); err != nil {
		return nil, fmt.Errorf("failed to load rules: %w", err)
	}

	return pe, nil
}

// AddRule добавляет правило с сохранением
func (pe *PersistentEngine) AddRule(rule Rule) (*Rule, error) {
	pe.mu.Lock()
	defer pe.mu.Unlock()

	created, err := pe.Engine.AddRule(rule)
	if err != nil {
		return nil, err
	}

	if err := pe.saveRules(); err != nil {
		// Откатываем добавление
		_ = pe.Engine.DeleteRule(created.ID)
		return nil, fmt.Errorf("failed to save rules: %w", err)
	}

	return created, nil
}

// UpdateRule обновляет правило с сохранением
func (pe *PersistentEngine) UpdateRule(id string, rule Rule) (*Rule, error) {
	pe.mu.Lock()
	defer pe.mu.Unlock()

	// Сохраняем старое правило для отката
	oldRule, err := pe.Engine.GetRule(id)
	if err != nil {
		return nil, err
	}

	updated, err := pe.Engine.UpdateRule(id, rule)
	if err != nil {
		return nil, err
	}

	if err := pe.saveRules(); err != nil {
		// Откатываем изменение
		_, _ = pe.Engine.UpdateRule(id, *oldRule)
		return nil, fmt.Errorf("failed to save rules: %w", err)
	}

	return updated, nil
}

// DeleteRule удаляет правило с сохранением
func (pe *PersistentEngine) DeleteRule(id string) error {
	pe.mu.Lock()
	defer pe.mu.Unlock()

	// Сохраняем правило для отката
	rule, err := pe.Engine.GetRule(id)
	if err != nil {
		return err
	}

	if err := pe.Engine.DeleteRule(id); err != nil {
		return err
	}

	if err := pe.saveRules(); err != nil {
		// Откатываем удаление
		_, _ = pe.Engine.AddRule(*rule)
		return fmt.Errorf("failed to save rules: %w", err)
	}

	return nil
}

// EnableRule включает правило с сохранением
func (pe *PersistentEngine) EnableRule(id string) error {
	pe.mu.Lock()
	defer pe.mu.Unlock()

	if err := pe.Engine.EnableRule(id); err != nil {
		return err
	}

	return pe.saveRules()
}

// DisableRule отключает правило с сохранением
func (pe *PersistentEngine) DisableRule(id string) error {
	pe.mu.Lock()
	defer pe.mu.Unlock()

	if err := pe.Engine.DisableRule(id); err != nil {
		return err
	}

	return pe.saveRules()
}

// saveRules сохраняет все правила
func (pe *PersistentEngine) saveRules() error {
	rules := pe.Engine.ListRules()
	return pe.storage.Save(rules)
}

// loadRules загружает правила из storage
func (pe *PersistentEngine) loadRules() error {
	rules, err := pe.storage.Load()
	if err != nil {
		return err
	}

	for _, rule := range rules {
		if _, err := pe.Engine.AddRule(rule); err != nil {
			// Логируем ошибку, но продолжаем загрузку
			continue
		}
	}

	return nil
}
