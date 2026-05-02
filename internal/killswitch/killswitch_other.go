//go:build !windows

package killswitch

import "proxyclient/internal/logger"

func IsEnabled() bool                  { return false }
func Enable(_ string, _ logger.Logger) {}
func Disable(_ logger.Logger)          {}
func CleanupOnStart(_ logger.Logger)   {}
func LoadState() (State, error)        { return State{}, nil }

type RuleState struct {
	Name string `json:"name"`
	ID   string `json:"id,omitempty"`
}

type State struct {
	Active                bool        `json:"active"`
	CreatedAt             string      `json:"created_at,omitempty"`
	CreatedByPID          int         `json:"created_by_pid,omitempty"`
	Rules                 []RuleState `json:"rules"`
	ExpectedCleanShutdown bool        `json:"expected_clean_shutdown"`
}
