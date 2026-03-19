// Package autorun управляет автозапуском приложения при входе в Windows.
// Использует HKCU\Software\Microsoft\Windows\CurrentVersion\Run — стандарт
// для всех proxy-клиентов (Clash Verge, Hiddify, Mihomo Party).
// HKCU (Current User) не требует прав администратора в отличие от HKLM.
package autorun

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

const (
	runKey  = `Software\Microsoft\Windows\CurrentVersion\Run`
	appName = "ProxyClient"
)

// Enable добавляет приложение в автозапуск Windows.
func Enable() error {
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
	key, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.WRITE)
	if err != nil {
		return fmt.Errorf("не удалось открыть ключ реестра: %w", err)
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
