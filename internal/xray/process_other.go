//go:build !windows

package xray

import (
	"fmt"
	"os/exec"
	"runtime"
)

func hideConsole(*exec.Cmd) {}

func sendCtrlBreak(int) error {
	return fmt.Errorf("CTRL_BREAK graceful shutdown is only available on Windows, current platform is %s", runtime.GOOS)
}
