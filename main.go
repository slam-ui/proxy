package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
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
	// --- НОВЫЙ БЛОК: Генерация конфига из шаблона и ключа ---
	templateFile := "config.template.json"
	secretFile := "secret.key"
	runtimeFile := "config.runtime.json"

	err := generateRuntimeConfig(templateFile, secretFile, runtimeFile)
	if err != nil {
		log.Fatalf("Ошибка при создании конфигурации: %v", err)
	}
	fmt.Println("Конфигурация успешно сгенерирована.")
	// --- КОНЕЦ НОВОГО БЛОКА ---

	fmt.Println("Включаем системный прокси...")
	err = setSystemProxy("127.0.0.1:10807", "<local>")
	if err != nil {
		fmt.Println("Не удалось включить системный прокси:", err)
	} else {
		fmt.Println("Прокси-сервер системы включен: 127.0.0.1:10807")
	}

	defer disableSystemProxy()

	fmt.Println("Запускаем приложение-обертку для Xray...")
	xrayPath := "./xray_core/xray.exe"
	// ИЗМЕНЕНИЕ: Используем новый, сгенерированный файл
	cmd := exec.Command(xrayPath, "-c", runtimeFile)
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

// --- НОВАЯ ФУНКЦИЯ: Генератор конфигурации ---
func generateRuntimeConfig(templatePath, secretPath, outputPath string) error {
	// 1. Читаем VLESS-ссылку из файла
	secretBytes, err := os.ReadFile(secretPath)
	if err != nil {
		return fmt.Errorf("не удалось прочитать файл с ключом '%s': %w", secretPath, err)
	}
	vlessURL := strings.TrimSpace(string(secretBytes))

	// 2. Парсим VLESS-ссылку
	parsedURL, err := url.Parse(vlessURL)
	if err != nil {
		return fmt.Errorf("неверный формат VLESS-ссылки: %w", err)
	}

	address := parsedURL.Hostname()
	port, _ := strconv.Atoi(parsedURL.Port())
	uuid := parsedURL.User.Username()
	queryParams := parsedURL.Query()
	sni := queryParams.Get("sni")
	publicKey := queryParams.Get("pbk")
	shortId := queryParams.Get("sid")

	// 3. Читаем шаблон конфигурации
	templateBytes, err := os.ReadFile(templatePath)
	if err != nil {
		return fmt.Errorf("не удалось прочитать файл шаблона '%s': %w", templatePath, err)
	}

	var config map[string]interface{}
	if err := json.Unmarshal(templateBytes, &config); err != nil {
		return fmt.Errorf("неверный формат JSON в шаблоне: %w", err)
	}

	// 4. Вставляем данные из ключа в структуру JSON
	// Это требует осторожной навигации по карте
	outbounds := config["outbounds"].([]interface{})
	firstOutbound := outbounds[0].(map[string]interface{})
	
	settings := firstOutbound["settings"].(map[string]interface{})
	vnext := settings["vnext"].([]interface{})
	firstVnext := vnext[0].(map[string]interface{})
	firstVnext["address"] = address
	firstVnext["port"] = port
	
	users := firstVnext["users"].([]interface{})
	firstUser := users[0].(map[string]interface{})
	firstUser["id"] = uuid

	streamSettings := firstOutbound["streamSettings"].(map[string]interface{})
	realitySettings := streamSettings["realitySettings"].(map[string]interface{})
	realitySettings["serverName"] = sni
	realitySettings["publicKey"] = publicKey
	realitySettings["shortId"] = shortId

	// 5. Сохраняем результат во временный файл
	outputBytes, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("ошибка при формировании итогового JSON: %w", err)
	}

	return os.WriteFile(outputPath, outputBytes, 0644)
}


// Функции setSystemProxy, disableSystemProxy, notifyWindows остаются без изменений
func setSystemProxy(proxyServer string, proxyOverride string) error {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.WRITE)
	if err != nil { return err }
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
	if err != nil { return err }
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