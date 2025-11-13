package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// GenerateRuntimeConfig создает config.runtime.json на основе шаблона и VLESS-ключа
func GenerateRuntimeConfig(templatePath, secretPath, outputPath string) error {
	// 1. Читаем ключ
	secretBytes, err := os.ReadFile(secretPath)
	if err != nil {
		return fmt.Errorf("не удалось прочитать файл с ключом '%s': %w", secretPath, err)
	}
	vlessURL := strings.TrimSpace(string(secretBytes))

	// 2. Парсим VLESS
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

	// 3. Читаем шаблон
	templateBytes, err := os.ReadFile(templatePath)
	if err != nil {
		return fmt.Errorf("не удалось прочитать файл шаблона '%s': %w", templatePath, err)
	}

	var config map[string]interface{}
	if err := json.Unmarshal(templateBytes, &config); err != nil {
		return fmt.Errorf("неверный формат JSON в шаблоне: %w", err)
	}

	// 4. Обновляем конфиг
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

	// 5. Сохраняем
	outputBytes, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("ошибка при формировании итогового JSON: %w", err)
	}

	return os.WriteFile(outputPath, outputBytes, 0644)
}
