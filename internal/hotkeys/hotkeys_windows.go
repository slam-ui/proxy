//go:build windows

package hotkeys

import (
	"fmt"

	"golang.org/x/sys/windows"
)

var (
	user32Hotkeys        = windows.NewLazySystemDLL("user32.dll")
	procRegisterHotKey   = user32Hotkeys.NewProc("RegisterHotKey")
	procUnregisterHotKey = user32Hotkeys.NewProc("UnregisterHotKey")
)

type Win32Registrar struct {
	HWND uintptr
}

func (r Win32Registrar) Register(id int, accelerator ParsedAccelerator) error {
	ret, _, err := procRegisterHotKey.Call(r.HWND, uintptr(id), uintptr(accelerator.Modifiers), uintptr(accelerator.Key))
	if ret == 0 {
		return fmt.Errorf("RegisterHotKey %s: %w", accelerator.Canonical, err)
	}
	return nil
}

func (r Win32Registrar) Unregister(id int) error {
	ret, _, err := procUnregisterHotKey.Call(r.HWND, uintptr(id))
	if ret == 0 {
		return fmt.Errorf("UnregisterHotKey %d: %w", id, err)
	}
	return nil
}
