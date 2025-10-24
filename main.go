package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

var (
	wininet, _           = syscall.LoadLibrary("wininet.dll")
	internetSetOption, _ = syscall.GetProcAddress(wininet, "InternetSetOptionW")
	INTERNET_OPTION_SETTINGS_CHANGED = 39
	INTERNET_OPTION_REFRESH          = 37
)

func main() {
	fmt.Println("Включаем системный прокси...")
	err := setSystemProxy("127.0.0.1:10807", "<local>")
	if err != nil {
		fmt.Println("Не удалось включить системный прокси:", err)
	} else {
		fmt.Println("Прокси-сервер системы включен: 127.0.0.1:10807")
	}

	defer disableSystemProxy()

	fmt.Println("Запускаем приложение-обертку для Xray...")
	xrayPath := "./xray_core/xray.exe"
	configPath := "./config.json"
	cmd := exec.Command(xrayPath, "-c", configPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err = cmd.Start()
	if err != nil {
		fmt.Println("Ошибка при запуске Xray:", err)
		return
	}

	fmt.Printf("Xray запущен успешно с PID: %d\n", cmd.Process.Pid)
	fmt.Println("Прокси работает. Нажмите Ctrl+C для остановки.")

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	fmt.Println("\nПолучен сигнал остановки. Отключаем системный прокси...")
	disableSystemProxy()

	fmt.Println("Завершаем процесс Xray...")
	err = cmd.Process.Kill()
	if err != nil {
		fmt.Println("Не удалось остановить процесс Xray:", err)
	} else {
		fmt.Println("Процесс Xray успешно остановлен.")
	}
}

func setSystemProxy(proxyServer string, proxyOverride string) error {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.WRITE)
	if err != nil {
		return err
	}
	defer key.Close()

	key.SetStringValue("ProxyServer", proxyServer)
	key.SetStringValue("ProxyOverride", proxyOverride)
	key.SetDWordValue("ProxyEnable", 1)

	notifyWindows()
	return nil
}

func disableSystemProxy() error {
	fmt.Println("Отключение прокси-сервера системы...")
	key, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.WRITE)
	if err != nil {
		return err
	}
	defer key.Close()

	key.SetDWordValue("ProxyEnable", 0)

	notifyWindows()
	fmt.Println("Системный прокси успешно отключен.")
	return nil
}

func notifyWindows() {
	syscall.SyscallN(internetSetOption, 0, uintptr(INTERNET_OPTION_SETTINGS_CHANGED), 0, 0)
	syscall.SyscallN(internetSetOption, 0, uintptr(INTERNET_OPTION_REFRESH), 0, 0)
}