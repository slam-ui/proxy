package proxy

import (
	"fmt"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

var (
	wininet, _                       = syscall.LoadLibrary("wininet.dll")
	internetSetOption, _             = syscall.GetProcAddress(wininet, "InternetSetOptionW")
	INTERNET_OPTION_SETTINGS_CHANGED = 39
	INTERNET_OPTION_REFRESH          = 37
)

// SetSystemProxy включает системный прокси
func SetSystemProxy(proxyServer string, proxyOverride string) error {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.WRITE)
	if err != nil {
		return err
	}
	defer func(key registry.Key) {
		err := key.Close()
		if err != nil {

		}
	}(key)

	if err := key.SetStringValue("ProxyServer", proxyServer); err != nil {
		return err
	}
	if err := key.SetStringValue("ProxyOverride", proxyOverride); err != nil {
		return err
	}
	if err := key.SetDWordValue("ProxyEnable", 1); err != nil {
		return err
	}

	notifyWindows()
	return nil
}

// DisableSystemProxy выключает системный прокси
func DisableSystemProxy() error {
	fmt.Println("Отключение прокси-сервера системы...")
	key, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.WRITE)
	if err != nil {
		return err
	}
	defer func(key registry.Key) {
		err := key.Close()
		if err != nil {

		}
	}(key)

	if err := key.SetDWordValue("ProxyEnable", 0); err != nil {
		return err
	}

	notifyWindows()
	fmt.Println("Системный прокси успешно отключен.")
	return nil
}

func notifyWindows() {
	syscall.SyscallN(internetSetOption, 0, uintptr(INTERNET_OPTION_SETTINGS_CHANGED), 0, 0)
	syscall.SyscallN(internetSetOption, 0, uintptr(INTERNET_OPTION_REFRESH), 0, 0)
}
