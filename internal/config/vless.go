package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
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
	Mux       bool // true если URL содержит ?mux=1 или ?multiplex=1
}

// vlessCache кэш разобранных параметров.
// OPT #6: parseVLESSKey читала файл с диска при каждом вызове GenerateSingBoxConfig.
// Функция вызывается при старте, при каждом apply (дважды), при ошибках.
// Кэш инвалидируется только когда mtime файла изменился — т.е. пользователь
// обновил secret.key. Аналог: GUI.for.SingBox кэширует конфиг по mtime.
var vlessCache struct {
	mu     sync.Mutex
	path   string
	mtime  time.Time
	params *VLESSParams
}

// parseVLESSKey читает и парсит VLESS URL из файла.
// При повторных вызовах с тем же файлом и неизменённым mtime возвращает кэш.
func parseVLESSKey(secretPath string) (*VLESSParams, error) {
	fi, err := os.Stat(secretPath)
	if err != nil {
		return nil, fmt.Errorf("не удалось прочитать файл '%s': %w", secretPath, err)
	}
	modTime := fi.ModTime()

	vlessCache.mu.Lock()
	if vlessCache.path == secretPath && vlessCache.params != nil && vlessCache.mtime.Equal(modTime) {
		cached := vlessCache.params
		vlessCache.mu.Unlock()
		return cached, nil
	}
	vlessCache.mu.Unlock()

	// Кэш устарел или отсутствует — читаем файл заново.
	params, err := readAndParseVLESS(secretPath)
	if err != nil {
		return nil, err
	}

	vlessCache.mu.Lock()
	vlessCache.path = secretPath
	vlessCache.mtime = modTime
	vlessCache.params = params
	vlessCache.mu.Unlock()

	return params, nil
}

// readAndParseVLESS выполняет фактическое чтение и парсинг файла.
func readAndParseVLESS(secretPath string) (*VLESSParams, error) {
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

	muxParam := queryParams.Get("mux")
	if muxParam == "" {
		muxParam = queryParams.Get("multiplex")
	}
	return &VLESSParams{
		Address:   parsedURL.Hostname(),
		Port:      port,
		UUID:      parsedURL.User.Username(),
		SNI:       queryParams.Get("sni"),
		PublicKey: queryParams.Get("pbk"),
		ShortID:   queryParams.Get("sid"),
		Flow:      queryParams.Get("flow"),
		Mux:       muxParam == "1" || muxParam == "true",
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
