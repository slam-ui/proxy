package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// VLESSParams параметры из VLESS URL
type VLESSParams struct {
	Address   string
	Port      int
	UUID      string
	SNI       string
	PublicKey string
	ShortID   string
}

// GenerateRuntimeConfig создает config.runtime.json на основе шаблона и VLESS-ключа
func GenerateRuntimeConfig(templatePath, secretPath, outputPath string) error {
	// 1. Читаем и парсим VLESS ключ
	vlessParams, err := parseVLESSKey(secretPath)
	if err != nil {
		return fmt.Errorf("ошибка парсинга VLESS ключа: %w", err)
	}

	// 2. Валидируем параметры
	if err := validateVLESSParams(vlessParams); err != nil {
		return fmt.Errorf("невалидные параметры VLESS: %w", err)
	}

	// 3. Читаем шаблон конфигурации
	config, err := loadTemplate(templatePath)
	if err != nil {
		return fmt.Errorf("ошибка загрузки шаблона: %w", err)
	}

	// 4. Обновляем конфигурацию
	if err := updateConfig(config, vlessParams); err != nil {
		return fmt.Errorf("ошибка обновления конфигурации: %w", err)
	}

	// 5. Сохраняем результат
	if err := saveConfig(config, outputPath); err != nil {
		return fmt.Errorf("ошибка сохранения конфигурации: %w", err)
	}

	return nil
}

// parseVLESSKey читает и парсит VLESS URL из файла
func parseVLESSKey(secretPath string) (*VLESSParams, error) {
	secretBytes, err := os.ReadFile(secretPath)
	if err != nil {
		return nil, fmt.Errorf("не удалось прочитать файл '%s': %w", secretPath, err)
	}

	vlessURL := strings.TrimSpace(string(secretBytes))
	if vlessURL == "" {
		return nil, fmt.Errorf("файл с ключом пуст")
	}

	parsedURL, err := url.Parse(vlessURL)
	if err != nil {
		return nil, fmt.Errorf("неверный формат URL: %w", err)
	}

	if parsedURL.Scheme != "vless" {
		return nil, fmt.Errorf("ожидается протокол 'vless', получен '%s'", parsedURL.Scheme)
	}

	port, err := strconv.Atoi(parsedURL.Port())
	if err != nil {
		return nil, fmt.Errorf("неверный порт '%s': %w", parsedURL.Port(), err)
	}

	queryParams := parsedURL.Query()

	return &VLESSParams{
		Address:   parsedURL.Hostname(),
		Port:      port,
		UUID:      parsedURL.User.Username(),
		SNI:       queryParams.Get("sni"),
		PublicKey: queryParams.Get("pbk"),
		ShortID:   queryParams.Get("sid"),
	}, nil
}

// validateVLESSParams проверяет параметры на корректность
func validateVLESSParams(params *VLESSParams) error {
	if params.Address == "" {
		return fmt.Errorf("отсутствует адрес сервера")
	}
	if params.Port <= 0 || params.Port > 65535 {
		return fmt.Errorf("некорректный порт: %d", params.Port)
	}
	if params.UUID == "" {
		return fmt.Errorf("отсутствует UUID")
	}
	if params.SNI == "" {
		return fmt.Errorf("отсутствует SNI")
	}
	if params.PublicKey == "" {
		return fmt.Errorf("отсутствует публичный ключ")
	}
	if params.ShortID == "" {
		return fmt.Errorf("отсутствует ShortID")
	}
	return nil
}

// loadTemplate загружает JSON шаблон
func loadTemplate(templatePath string) (map[string]interface{}, error) {
	templateBytes, err := os.ReadFile(templatePath)
	if err != nil {
		return nil, fmt.Errorf("не удалось прочитать файл '%s': %w", templatePath, err)
	}

	var config map[string]interface{}
	if err := json.Unmarshal(templateBytes, &config); err != nil {
		return nil, fmt.Errorf("неверный формат JSON: %w", err)
	}

	return config, nil
}

// updateConfig обновляет конфигурацию параметрами из VLESS
func updateConfig(config map[string]interface{}, params *VLESSParams) error {
	outbounds, ok := config["outbounds"].([]interface{})
	if !ok || len(outbounds) == 0 {
		return fmt.Errorf("отсутствует массив outbounds в конфигурации")
	}

	firstOutbound, ok := outbounds[0].(map[string]interface{})
	if !ok {
		return fmt.Errorf("некорректная структура outbound")
	}

	// Обновляем settings
	settings, ok := firstOutbound["settings"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("отсутствует settings в outbound")
	}

	vnext, ok := settings["vnext"].([]interface{})
	if !ok || len(vnext) == 0 {
		return fmt.Errorf("отсутствует vnext в settings")
	}

	firstVnext, ok := vnext[0].(map[string]interface{})
	if !ok {
		return fmt.Errorf("некорректная структура vnext")
	}

	firstVnext["address"] = params.Address
	firstVnext["port"] = params.Port

	users, ok := firstVnext["users"].([]interface{})
	if !ok || len(users) == 0 {
		return fmt.Errorf("отсутствует users в vnext")
	}

	firstUser, ok := users[0].(map[string]interface{})
	if !ok {
		return fmt.Errorf("некорректная структура user")
	}

	firstUser["id"] = params.UUID

	// Обновляем streamSettings
	streamSettings, ok := firstOutbound["streamSettings"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("отсутствует streamSettings в outbound")
	}

	realitySettings, ok := streamSettings["realitySettings"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("отсутствует realitySettings в streamSettings")
	}

	realitySettings["serverName"] = params.SNI
	realitySettings["publicKey"] = params.PublicKey
	realitySettings["shortId"] = params.ShortID

	return nil
}

// saveConfig сохраняет конфигурацию в файл
func saveConfig(config map[string]interface{}, outputPath string) error {
	outputBytes, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("ошибка формирования JSON: %w", err)
	}

	if err := os.WriteFile(outputPath, outputBytes, 0644); err != nil {
		return fmt.Errorf("не удалось записать файл '%s': %w", outputPath, err)
	}

	return nil
}
