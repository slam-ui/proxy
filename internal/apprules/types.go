package apprules

import "time"

// Action тип действия для правила
type Action string

const (
	ActionDirect Action = "DIRECT" // Без прокси
	ActionProxy  Action = "PROXY"  // Через прокси
	ActionBlock  Action = "BLOCK"  // Блокировать
)

// IsValid проверяет валидность действия
func (a Action) IsValid() bool {
	switch a {
	case ActionDirect, ActionProxy, ActionBlock:
		return true
	default:
		return false
	}
}

// Rule правило для приложения
type Rule struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Pattern   string    `json:"pattern"`    // "firefox.exe", "*.exe", "/path/to/app.exe"
	Action    Action    `json:"action"`     // DIRECT, PROXY, BLOCK
	ProxyAddr string    `json:"proxy_addr"` // "127.0.0.1:10807"
	Priority  int       `json:"priority"`   // Выше = важнее
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ProcessInfo информация о процессе
type ProcessInfo struct {
	PID         int       `json:"pid"`
	Name        string    `json:"name"`
	Executable  string    `json:"executable"` // Полный путь
	ParentPID   int       `json:"parent_pid"`
	StartTime   time.Time `json:"start_time"`
	RuleID      string    `json:"rule_id,omitempty"`
	ProxyStatus string    `json:"proxy_status"` // DIRECT, PROXIED, BLOCKED, UNKNOWN
}

// RuleMatch результат сопоставления правила
type RuleMatch struct {
	Matched bool
	Rule    *Rule
}
