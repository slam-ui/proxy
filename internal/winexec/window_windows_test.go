//go:build windows

package winexec

import (
	"os/exec"
	"syscall"
	"testing"
)

func TestHideWindowSetsWindowsFlags(t *testing.T) {
	cmd := exec.Command("cmd")

	HideWindow(cmd)

	if cmd.SysProcAttr == nil {
		t.Fatal("SysProcAttr was not set")
	}
	if cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Fatalf("CreationFlags = %#x, want CREATE_NO_WINDOW", cmd.SysProcAttr.CreationFlags)
	}
	if !cmd.SysProcAttr.HideWindow {
		t.Fatal("HideWindow was not enabled")
	}
}

func TestHideWindowPreservesExistingCreationFlags(t *testing.T) {
	const existingFlag = 0x00000200
	cmd := exec.Command("cmd")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: existingFlag}

	HideWindow(cmd)

	if cmd.SysProcAttr.CreationFlags&existingFlag == 0 {
		t.Fatalf("CreationFlags = %#x, lost existing flag %#x", cmd.SysProcAttr.CreationFlags, existingFlag)
	}
	if cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Fatalf("CreationFlags = %#x, want CREATE_NO_WINDOW", cmd.SysProcAttr.CreationFlags)
	}
}
