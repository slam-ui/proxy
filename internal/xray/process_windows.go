//go:build windows

package xray

import (
	"fmt"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// kernel32 для CTRL_BREAK graceful shutdown.
// Используем LazySystemDLL — загрузка при первом вызове только из System32.
var (
	kernel32          = windows.NewLazySystemDLL("kernel32.dll")
	procGenCtrlEvt    = kernel32.NewProc("GenerateConsoleCtrlEvent")
	procAttachConsole = kernel32.NewProc("AttachConsole")
	procFreeConsole   = kernel32.NewProc("FreeConsole")
)

func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x08000000,
		HideWindow:    true,
	}
}

// sendCtrlBreak посылает CTRL_BREAK в процесс pid.
// Sing-box перехватывает это событие и выполняет graceful shutdown:
// сохраняет DNS-кэш, закрывает TUN-адаптер — что снижает вероятность
// "file already exists" при следующем старте.
func sendCtrlBreak(pid int) error {
	_, _, _ = procAttachConsole.Call(uintptr(pid))
	defer func() { _, _, _ = procFreeConsole.Call() }()
	ret, _, err := procGenCtrlEvt.Call(
		syscall.CTRL_BREAK_EVENT,
		uintptr(pid),
	)
	if ret == 0 {
		return fmt.Errorf("GenerateConsoleCtrlEvent failed: %w", err)
	}
	return nil
}
