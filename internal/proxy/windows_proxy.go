package proxy

import (
	"fmt"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

const (
	internetOptionSettingsChanged = 39
	internetOptionRefresh         = 37
	registryPath                  = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`
)

var (
	modWininet            = syscall.NewLazyDLL("wininet.dll")
	procInternetSetOption = modWininet.NewProc("InternetSetOptionW")
)

// setSystemProxy включает системный прокси
func setSystemProxy(proxyServer string, proxyOverride string) error {
	key, err := registry.OpenKey(registry.CURRENT_USER, registryPath, registry.WRITE)
	if err != nil {
		return fmt.Errorf("не удалось открыть ключ реестра: %w", err)
	}
	defer key.Close()

	if err := key.SetStringValue("ProxyServer", proxyServer); err != nil {
		return fmt.Errorf("не удалось установить ProxyServer: %w", err)
	}

	if err := key.SetStringValue("ProxyOverride", proxyOverride); err != nil {
		return fmt.Errorf("не удалось установить ProxyOverride: %w", err)
	}

	if err := key.SetDWordValue("ProxyEnable", 1); err != nil {
		return fmt.Errorf("не удалось установить ProxyEnable: %w", err)
	}

	if err := notifyWindows(); err != nil {
		return fmt.Errorf("не удалось уведомить систему об изменениях: %w", err)
	}

	return nil
}

// disableSystemProxy выключает системный прокси
func disableSystemProxy() error {
	key, err := registry.OpenKey(registry.CURRENT_USER, registryPath, registry.WRITE)
	if err != nil {
		return fmt.Errorf("не удалось открыть ключ реестра: %w", err)
	}
	defer key.Close()

	if err := key.SetDWordValue("ProxyEnable", 0); err != nil {
		return fmt.Errorf("не удалось установить ProxyEnable: %w", err)
	}

	if err := notifyWindows(); err != nil {
		return fmt.Errorf("не удалось уведомить систему об изменениях: %w", err)
	}

	return nil
}

// getSystemProxyState читает текущее состояние системного прокси из реестра.
// BUG FIX #6: используется при создании Manager чтобы синхронизировать
// in-memory состояние с реальным состоянием реестра Windows.
// Защита от ситуации когда приложение упало с включённым прокси — при следующем
// запуске manager.enabled=false хотя реестр говорит ProxyEnable=1.
func getSystemProxyState() (enabled bool, address string) {
	key, err := registry.OpenKey(registry.CURRENT_USER, registryPath, registry.READ)
	if err != nil {
		return false, ""
	}
	defer key.Close()

	val, _, err := key.GetIntegerValue("ProxyEnable")
	if err != nil || val == 0 {
		return false, ""
	}

	addr, _, err := key.GetStringValue("ProxyServer")
	if err != nil {
		return true, ""
	}
	return true, addr
}

// notifyWindows уведомляет Windows об изменении настроек прокси
func notifyWindows() error {
	// InternetSetOption(NULL, INTERNET_OPTION_SETTINGS_CHANGED, NULL, 0)
	ret, _, _ := procInternetSetOption.Call(
		0, // hInternet = NULL
		uintptr(internetOptionSettingsChanged),
		0, // lpBuffer = NULL
		0, // dwBufferLength = 0
	)
	if ret == 0 {
		return fmt.Errorf("InternetSetOption (SETTINGS_CHANGED) failed")
	}

	// InternetSetOption(NULL, INTERNET_OPTION_REFRESH, NULL, 0)
	ret, _, _ = procInternetSetOption.Call(
		0, // hInternet = NULL
		uintptr(internetOptionRefresh),
		0, // lpBuffer = NULL
		0, // dwBufferLength = 0
	)
	if ret == 0 {
		return fmt.Errorf("InternetSetOption (REFRESH) failed")
	}

	return nil
}


