//go:build !windows

package process

import (
	"fmt"

	"proxyclient/internal/apprules"
	"proxyclient/internal/logger"
)

type Launcher interface {
	Launch(executable string, rule *apprules.Rule, args ...string) (*LaunchResult, error)
	LaunchWithRule(executable string, ruleID string, args ...string) (*LaunchResult, error)
}

type LaunchResult struct {
	PID        int
	Executable string
	RuleID     string
	ProxyAddr  string
	Action     apprules.Action
}

type launcher struct{}

func NewLauncher(logger.Logger, apprules.Engine) Launcher {
	return launcher{}
}

func (launcher) Launch(string, *apprules.Rule, ...string) (*LaunchResult, error) {
	return nil, fmt.Errorf("process launcher is only available on Windows")
}

func (launcher) LaunchWithRule(string, string, ...string) (*LaunchResult, error) {
	return nil, fmt.Errorf("process launcher is only available on Windows")
}
