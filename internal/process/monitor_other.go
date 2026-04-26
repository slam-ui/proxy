//go:build !windows

package process

import (
	"fmt"

	"proxyclient/internal/apprules"
	"proxyclient/internal/logger"
)

type Monitor interface {
	Start() error
	Stop() error
	GetProcesses() []apprules.ProcessInfo
	GetProcess(pid int) (*apprules.ProcessInfo, error)
	Refresh() error
}

type monitor struct{}

func NewMonitor(logger.Logger) Monitor {
	return monitor{}
}

func NewMonitorWithEngine(logger.Logger, apprules.Engine) Monitor {
	return monitor{}
}

func (monitor) Start() error { return nil }
func (monitor) Stop() error  { return nil }
func (monitor) GetProcesses() []apprules.ProcessInfo {
	return nil
}
func (monitor) GetProcess(pid int) (*apprules.ProcessInfo, error) {
	return nil, fmt.Errorf("process monitor is only available on Windows")
}
func (monitor) Refresh() error { return nil }
