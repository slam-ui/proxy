//go:build windows

package notification

import (
	"os/exec"
	"syscall"
)

// Send показывает Windows-уведомление в системном трее.
// Использует PowerShell BurntToast или встроенный WScript.Shell balloon tip.
// Не блокирует — запускается в фоновой горутине.
func Send(title, message string) {
	go func() {
		// Пробуем через Windows Runtime Toast API (Win10+)
		script := `
$ErrorActionPreference='SilentlyContinue'
Add-Type -AssemblyName System.Windows.Forms
$notify = New-Object System.Windows.Forms.NotifyIcon
$notify.Icon = [System.Drawing.SystemIcons]::Application
$notify.Visible = $true
$notify.ShowBalloonTip(3000, '` + escapePS(title) + `', '` + escapePS(message) + `', [System.Windows.Forms.ToolTipIcon]::Info)
Start-Sleep -Milliseconds 4000
$notify.Dispose()
`
		cmd := exec.Command("powershell", "-WindowStyle", "Hidden", "-NonInteractive", "-Command", script)
		cmd.SysProcAttr = &syscall.SysProcAttr{
			CreationFlags: 0x08000000, // CREATE_NO_WINDOW
			HideWindow:    true,
		}
		_ = cmd.Start()
	}()
}

// escapePS экранирует строку для вставки в PowerShell single-quoted строку
func escapePS(s string) string {
	result := ""
	for _, c := range s {
		if c == '\'' {
			result += "''"
		} else {
			result += string(c)
		}
	}
	return result
}
