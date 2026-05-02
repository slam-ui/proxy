//go:build !windows

package hotkeys

import "fmt"

type Win32Registrar struct{}

func (Win32Registrar) Register(id int, accelerator ParsedAccelerator) error {
	return fmt.Errorf("global hotkeys are only supported on Windows")
}

func (Win32Registrar) Unregister(id int) error {
	return nil
}
