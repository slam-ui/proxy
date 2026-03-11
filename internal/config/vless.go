package config

import (
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
	Flow      string
}

// parseVLESSKey читает и парсит VLESS URL из файла
func parseVLESSKey(secretPath string) (*VLESSParams, error) {
	secretBytes, err := os.ReadFile(secretPath)
	if err != nil {
		return nil, fmt.Errorf("не удалось прочитать файл '%s': %w", secretPath, err)
	}

	// Убираем BOM (U+FEFF) — Блокнот Windows добавляет его при сохранении
	// в UTF-8, что ломает парсинг URL ("first path segment cannot contain colon").
	vlessURL := strings.TrimPrefix(strings.TrimSpace(string(secretBytes)), "\ufeff")

	// Игнорируем строки-комментарии, берём первую непустую строку без #
	for _, line := range strings.Split(vlessURL, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			vlessURL = line
			break
		}
	}

	if vlessURL == "" || strings.HasPrefix(vlessURL, "#") {
		return nil, fmt.Errorf("файл с ключом пуст или содержит только комментарии")
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
		Flow:      queryParams.Get("flow"),
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
