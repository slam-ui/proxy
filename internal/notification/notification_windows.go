//go:build windows

package notification

import (
	"os/exec"
	"syscall"
)

// notifySem ограничивает количество одновременных PowerShell-процессов уведомлений.
// BUG FIX #NEW-F: при TUN retry loop вызывается до 10+ Send() подряд (~2 уведомления
// × 5 попыток). Каждый PowerShell-процесс блокирует горутину на ~4.5с (Start-Sleep 4s).
// Без семафора: 10 горутин × 4.5с = 45с жизни PS-процессов одновременно.
// С буфером 2: новые уведомления пропускаются если уже показываются 2 — пользователь
// всё равно не успевает прочитать их все, зато система не перегружается.
var notifySem = make(chan struct{}, 2)

// Send показывает Windows-уведомление в системном трее.
// Использует PowerShell + System.Windows.Forms.NotifyIcon (Win10+).
// Не блокирует — запускается в фоновой горутине.
// При переполнении (>2 одновременных) уведомление пропускается без ошибки.
func Send(title, message string) {
	go func() {
		// Пробуем занять слот — если заняты оба, уведомление пропускаем.
		select {
		case notifySem <- struct{}{}:
			defer func() { <-notifySem }()
		default:
			return // уже показываются 2 уведомления — пропускаем
		}

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
		// BUG FIX #B-30: вызываем Wait() чтобы освободить HANDLE процесса.
		// Start() без Wait() оставляет kernel HANDLE открытым до завершения
		// родительского процесса — при частых уведомлениях накапливаются утечки.
		if err := cmd.Start(); err == nil {
			_ = cmd.Wait()
		}
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
