package apprules

import (
	"crypto/rand"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Engine интерфейс для управления правилами
type Engine interface {
	AddRule(rule Rule) (*Rule, error)
	GetRule(id string) (*Rule, error)
	UpdateRule(id string, rule Rule) (*Rule, error)
	DeleteRule(id string) error
	EnableRule(id string) error
	DisableRule(id string) error
	ListRules() []Rule
	Match(processName string) RuleMatch
	FindMatchingRule(processName string) RuleMatch
}

// engine реализация Engine
type engine struct {
	rules   map[string]*Rule
	matcher Matcher
	mu      sync.RWMutex
}

// NewEngine создаёт новый engine
func NewEngine() Engine {
	return &engine{
		rules:   make(map[string]*Rule),
		matcher: NewMatcher(),
	}
}

// newID генерирует уникальный ID без внешних зависимостей
func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// AddRule добавляет новое правило
func (e *engine) AddRule(rule Rule) (*Rule, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := validateRule(rule); err != nil {
		return nil, fmt.Errorf("invalid rule: %w", err)
	}

	now := time.Now()
	rule.ID = newID()
	rule.CreatedAt = now
	rule.UpdatedAt = now

	e.rules[rule.ID] = &rule
	return &rule, nil
}

// GetRule возвращает правило по ID
func (e *engine) GetRule(id string) (*Rule, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	rule, ok := e.rules[id]
	if !ok {
		return nil, fmt.Errorf("rule not found: %s", id)
	}

	copy := *rule
	return &copy, nil
}

// UpdateRule обновляет правило
func (e *engine) UpdateRule(id string, rule Rule) (*Rule, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	existing, ok := e.rules[id]
	if !ok {
		return nil, fmt.Errorf("rule not found: %s", id)
	}

	if err := validateRule(rule); err != nil {
		return nil, fmt.Errorf("invalid rule: %w", err)
	}

	rule.ID = id
	rule.CreatedAt = existing.CreatedAt
	rule.UpdatedAt = time.Now()

	e.rules[id] = &rule
	return &rule, nil
}

// DeleteRule удаляет правило
func (e *engine) DeleteRule(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if _, ok := e.rules[id]; !ok {
		return fmt.Errorf("rule not found: %s", id)
	}

	delete(e.rules, id)
	return nil
}

// EnableRule включает правило
func (e *engine) EnableRule(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	rule, ok := e.rules[id]
	if !ok {
		return fmt.Errorf("rule not found: %s", id)
	}

	rule.Enabled = true
	rule.UpdatedAt = time.Now()
	return nil
}

// DisableRule отключает правило
func (e *engine) DisableRule(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	rule, ok := e.rules[id]
	if !ok {
		return fmt.Errorf("rule not found: %s", id)
	}

	rule.Enabled = false
	rule.UpdatedAt = time.Now()
	return nil
}

// ListRules возвращает все правила, отсортированные по приоритету
func (e *engine) ListRules() []Rule {
	e.mu.RLock()
	defer e.mu.RUnlock()

	rules := make([]Rule, 0, len(e.rules))
	for _, r := range e.rules {
		rules = append(rules, *r)
	}

	// Сортируем по приоритету (выше = важнее)
	sort.Slice(rules, func(i, j int) bool {
		return rules[i].Priority > rules[j].Priority
	})

	return rules
}

// Match находит первое подходящее правило для процесса
func (e *engine) Match(processName string) RuleMatch {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// Собираем включённые правила и сортируем по приоритету
	rules := make([]*Rule, 0, len(e.rules))
	for _, r := range e.rules {
		if r.Enabled {
			rules = append(rules, r)
		}
	}

	sort.Slice(rules, func(i, j int) bool {
		return rules[i].Priority > rules[j].Priority
	})

	for _, rule := range rules {
		if e.matcher.Match(rule.Pattern, processName) {
			copy := *rule
			return RuleMatch{Matched: true, Rule: &copy}
		}
	}

	return RuleMatch{Matched: false}
}

// validateRule проверяет правило на корректность
func validateRule(rule Rule) error {
	if rule.Pattern == "" {
		return fmt.Errorf("pattern is required")
	}
	if !rule.Action.IsValid() {
		return fmt.Errorf("invalid action: %s", rule.Action)
	}
	if rule.Action == ActionProxy && rule.ProxyAddr == "" {
		return fmt.Errorf("proxy_addr is required for PROXY action")
	}
	return nil
}

// FindMatchingRule — алиас для Match, используется в handlers
func (e *engine) FindMatchingRule(processName string) RuleMatch {
	return e.Match(processName)
}
