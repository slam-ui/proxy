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
	rules map[string]*Rule
	// BUG FIX #12: sorted кэширует rules в порядке убывания Priority.
	// Ранее Match() вызывал sort.Slice на каждый вызов (O(n log n)).
	// Теперь сортировка выполняется только в rebuildSorted() при мутациях.
	sorted  []*Rule
	matcher Matcher
	mu      sync.RWMutex
}

// rebuildSorted пересобирает и сортирует срез enabled-правил.
// Вызывается при любой мутации (Add/Update/Delete/Enable/Disable).
// ВАЖНО: должен вызываться под e.mu.Lock().
func (e *engine) rebuildSorted() {
	s := make([]*Rule, 0, len(e.rules))
	for _, r := range e.rules {
		if r.Enabled {
			s = append(s, r)
		}
	}
	sort.Slice(s, func(i, j int) bool {
		return s[i].Priority > s[j].Priority
	})
	e.sorted = s
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

	if err := validateNewRule(rule); err != nil {
		return nil, fmt.Errorf("invalid rule: %w", err)
	}

	now := time.Now()
	rule.ID = newID()
	rule.CreatedAt = now
	rule.UpdatedAt = now
	// OPT #1: нормализуем паттерн один раз при сохранении.
	rule.Pattern = NormalizePattern(rule.Pattern)

	e.rules[rule.ID] = &rule
	e.rebuildSorted()
	return &rule, nil
}

// restoreRule загружает правило с существующим ID (используется при загрузке из файла)
func (e *engine) restoreRule(rule Rule) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := validateRule(rule); err != nil {
		return fmt.Errorf("invalid rule: %w", err)
	}
	if rule.ID == "" {
		rule.ID = newID()
	}
	rule.Pattern = NormalizePattern(rule.Pattern)
	e.rules[rule.ID] = &rule
	e.rebuildSorted()
	return nil
}

// GetRule возвращает правило по ID
func (e *engine) GetRule(id string) (*Rule, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	rule, ok := e.rules[id]
	if !ok {
		return nil, fmt.Errorf("rule not found: %s", id)
	}

	ruleCopy := *rule
	return &ruleCopy, nil
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
	rule.Pattern = NormalizePattern(rule.Pattern)

	e.rules[id] = &rule
	e.rebuildSorted()
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
	e.rebuildSorted()
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
	e.rebuildSorted()
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
	e.rebuildSorted()
	return nil
}

// ListRules возвращает все правила, отсортированные по приоритету (для API)
func (e *engine) ListRules() []Rule {
	e.mu.RLock()
	defer e.mu.RUnlock()

	rules := make([]Rule, 0, len(e.rules))
	for _, r := range e.rules {
		rules = append(rules, *r)
	}

	sort.Slice(rules, func(i, j int) bool {
		return rules[i].Priority > rules[j].Priority
	})

	return rules
}

// listRulesUnsorted возвращает все правила без сортировки — только для сохранения.
// OPT #3: ListRules() делает sort.Slice при каждом Save(). Порядок в JSON-файле
// не важен — при загрузке правила сортируются через rebuildSorted().
func (e *engine) listRulesUnsorted() []Rule {
	e.mu.RLock()
	defer e.mu.RUnlock()
	rules := make([]Rule, 0, len(e.rules))
	for _, r := range e.rules {
		rules = append(rules, *r)
	}
	return rules
}

// Match находит первое подходящее правило для процесса.
// BUG FIX #12: сортировка вынесена в rebuildSorted() которая вызывается
// только при мутациях. Match теперь просто читает уже отсортированный срез —
// O(n) вместо O(n log n) на каждый вызов от process monitor.
// BUG FIX #RACE: RLock удерживается на всё время итерации (включая *rule copy).
// Ранее: e.mu.RUnlock() до цикла — DisableRule мог записать rule.Enabled=false
// пока Match читал поля того же Rule-объекта → data race под -race флагом.
// Matcher.Match не захватывает никаких мьютексов, поэтому deadlock невозможен.
func (e *engine) Match(processName string) RuleMatch {
	e.mu.RLock()
	defer e.mu.RUnlock()

	for _, rule := range e.sorted {
		if e.matcher.Match(rule.Pattern, processName) {
			ruleCopy := *rule
			return RuleMatch{Matched: true, Rule: &ruleCopy}
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
	return nil
}

// validateNewRule дополнительные проверки для правил создаваемых через API
func validateNewRule(rule Rule) error {
	return validateRule(rule)
}

// FindMatchingRule — алиас для Match, используется в handlers
func (e *engine) FindMatchingRule(processName string) RuleMatch {
	return e.Match(processName)
}
