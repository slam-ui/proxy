//go:build windows

package notification

import (
	"os"
	"os/exec"
	"strings"
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

		title, message, iconName := normalizeNotification(title, message)
		exePath, _ := os.Executable()

		// Windows Forms NotifyIcon даёт нативное Windows-уведомление.
		// Заголовок/текст нормализуем здесь, чтобы все вызовы говорили языком UI.
		script := `
$ErrorActionPreference='SilentlyContinue'
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
$notify = New-Object System.Windows.Forms.NotifyIcon
try {
  $notify.Icon = [System.Drawing.Icon]::ExtractAssociatedIcon('` + escapePS(exePath) + `')
} catch {
  $notify.Icon = [System.Drawing.SystemIcons]::Information
}
$notify.Visible = $true
$notify.ShowBalloonTip(3600, '` + escapePS(title) + `', '` + escapePS(message) + `', [System.Windows.Forms.ToolTipIcon]::` + iconName + `)
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

func normalizeNotification(title, message string) (string, string, string) {
	title = strings.TrimSpace(title)
	message = strings.TrimSpace(message)
	combined := strings.ToLower(title + " " + message)

	iconName := "Info"
	if strings.Contains(combined, "ошиб") ||
		strings.Contains(combined, "error") ||
		strings.Contains(combined, "failed") ||
		strings.Contains(combined, "не удалось") ||
		strings.Contains(combined, "занят") ||
		strings.Contains(combined, "упал") {
		iconName = "Error"
	} else if strings.Contains(combined, "деграда") ||
		strings.Contains(combined, "degraded") ||
		strings.Contains(combined, "retry") ||
		strings.Contains(combined, "restart") ||
		strings.Contains(combined, "повтор") ||
		strings.Contains(combined, "перезапуск") ||
		strings.Contains(combined, "подожд") {
		iconName = "Warning"
	}

	if title == "" || title == "SafeSky" || strings.EqualFold(title, "SafeSky — ошибка") {
		switch iconName {
		case "Error":
			title = notificationT("notification.title.action")
		case "Warning":
			title = notificationT("notification.title.recovery")
		default:
			title = "SafeSky"
		}
	}

	replacements := []struct {
		old string
		key string
	}{
		{"Прокси включён ✓", "notification.proxy.enabled"},
		{"Прокси отключён", "notification.proxy.disabled"},
		{"Прокси готов ✓", "notification.proxy.ready"},
		{"Прокси готов к ручному включению", "notification.proxy.ready_manual"},
		{"Инициализация... подождите", "notification.initializing"},
		{"Загружаем sing-box.exe...", "notification.engine.downloading"},
		{"Повторная загрузка sing-box...", "notification.engine.redownloading"},
		{"sing-box.exe загружен ✓", "notification.engine.downloaded"},
		{"sing-box не запустился. Проверьте лог.", "notification.engine.not_started"},
		{"Не удалось запустить sing-box. Проверьте лог.", "notification.engine.start_failed"},
		{"sing-box упал. Проверьте лог и перезапустите.", "notification.engine.crashed"},
		{"Вставьте VLESS-ссылку в secret.key", "notification.server.add_key"},
		{"Не удалось переключить сервер", "notification.server.switch_failed"},
	}
	for _, repl := range replacements {
		message = strings.ReplaceAll(message, repl.old, notificationT(repl.key))
	}
	message = strings.ReplaceAll(message, "Перезапуск TUN...", notificationT("notification.tun.restarting"))
	message = strings.ReplaceAll(message, "Перезапущен ✓", notificationT("notification.tun.restarted"))
	message = strings.ReplaceAll(message, "sing-box.exe", notificationT("notification.engine.name"))
	message = strings.ReplaceAll(message, "sing-box", notificationT("notification.engine.name"))
	message = strings.ReplaceAll(message, "Прокси", notificationT("notification.tunnel"))
	message = strings.ReplaceAll(message, "прокси", notificationT("notification.tunnel.lower"))

	if strings.TrimSpace(message) == "" {
		message = notificationT("notification.status.updated")
	}
	return title, message, iconName
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
