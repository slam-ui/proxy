//go:build windows

// Package autorun управляет автозапуском приложения при входе в Windows.
// Использует HKCU\Software\Microsoft\Windows\CurrentVersion\Run — стандарт
// для всех proxy-клиентов (Clash Verge, Hiddify, Mihomo Party).
// HKCU (Current User) не требует прав администратора в отличие от HKLM.
package autorun

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

const (
	runKey  = `Software\Microsoft\Windows\CurrentVersion\Run`
	appName = "ProxyClient"
)

// Enable добавляет приложение в автозапуск Windows.
// Сначала пробует Task Scheduler с повышенными привилегиями (Windows 11 24H2).
// Fallback: реестр HKCU\Run (без UAC elevation).
func Enable() error {
	if err := enableViaTaskScheduler(); err == nil {
		return nil
	}
	return enableViaRegistry()
}

func enableViaTaskScheduler() error {
	exePath, err := exeAbsPath()
	if err != nil {
		return err
	}
	// /RL HIGHEST — запуск с максимальными привилегиями пользователя (без UAC prompt).
	// /IT — только при интерактивной сессии. /F — перезаписать если уже существует.
	// Источник: v2rayN issues #7470 #7648 — на Windows 11 24H2 HKCU\Run не даёт elevation.
	cmd := exec.Command("schtasks",
		"/Create", "/TN", `ProxyClient\Autostart`,
		"/SC", "ONLOGON",
		"/TR", fmt.Sprintf(`"%s"`, exePath),
		"/RL", "HIGHEST",
		"/F", "/IT",
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
	return cmd.Run()
}

func enableViaRegistry() error {
	exePath, err := exeAbsPath()
	if err != nil {
		return err
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.WRITE)
	if err != nil {
		return fmt.Errorf("не удалось открыть ключ реестра: %w", err)
	}
	defer key.Close()
	if err := key.SetStringValue(appName, exePath); err != nil {
		return fmt.Errorf("не удалось записать ключ автозапуска: %w", err)
	}
	return nil
}

// Disable убирает приложение из автозапуска Windows.
func Disable() error {
	// Удаляем из Task Scheduler
	cmd := exec.Command("schtasks", "/Delete", "/TN", `ProxyClient\Autostart`, "/F")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
	_ = cmd.Run() // ошибка если задачи нет — игнорируем

	// Удаляем из реестра
	key, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.WRITE)
	if err != nil {
		return nil // если ключа нет — всё ок
	}
	defer key.Close()
	if err := key.DeleteValue(appName); err != nil && err != registry.ErrNotExist {
		return fmt.Errorf("не удалось удалить ключ автозапуска: %w", err)
	}
	return nil
}

// IsEnabled возвращает true если приложение зарегистрировано в автозапуске
// и путь совпадает с текущим .exe.
func IsEnabled() bool {
	// Проверяем Task Scheduler
	cmd := exec.Command("schtasks", "/Query", "/TN", `ProxyClient\Autostart`, "/FO", "LIST")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
	if err := cmd.Run(); err == nil {
		return true
	}
	// Проверяем реестр
	key, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.READ)
	if err != nil {
		return false
	}
	defer key.Close()
	val, _, err := key.GetStringValue(appName)
	if err != nil {
		return false
	}
	exe, err := exeAbsPath()
	if err != nil {
		return false
	}
	return val == exe
}

func exeAbsPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("не удалось определить путь к .exe: %w", err)
	}
	return filepath.Abs(exe)
}
