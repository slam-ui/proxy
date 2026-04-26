package config

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"proxyclient/internal/dpapi"
	"proxyclient/internal/fileutil"
)

const dpapiMagic = "DPAPI:"

// VLESSParams параметры из VLESS URL
type VLESSParams struct {
	Address      string
	Port         int
	UUID         string
	SNI          string
	PublicKey    string
	ShortID      string
	Flow         string
	Mux          bool // true если URL содержит ?mux=1 или ?multiplex=1
	Fragment     bool
	FragmentSize string
	Fingerprint  string
}

// VLESSCache кэш разобранных параметров VLESS.
// Zero-value VLESSCache{} создаёт готовый к использованию кэш.
// Все методы потокобезопасны.
//
// A-9: вынесен в именованный тип вместо пакетного анонимного struct —
// тесты создают VLESSCache{} локально и не разделяют общее состояние.
type VLESSCache struct {
	mu     sync.Mutex
	path   string
	mtime  time.Time
	params *VLESSParams
}

// Parse читает и парсит VLESS URL из файла secretPath.
// При повторных вызовах с тем же файлом и неизменённым mtime возвращает кэш.
// Потокобезопасен.
func (c *VLESSCache) Parse(secretPath string) (*VLESSParams, error) {
	fi, err := os.Stat(secretPath)
	if err != nil {
		return nil, fmt.Errorf("не удалось прочитать файл '%s': %w", secretPath, err)
	}
	modTime := fi.ModTime()

	c.mu.Lock()
	if c.path == secretPath && c.params != nil && c.mtime.Equal(modTime) {
		cached := c.params
		c.mu.Unlock()
		return cached, nil
	}
	c.mu.Unlock()

	// Кэш устарел или отсутствует — читаем файл заново.
	params, err := readAndParseVLESS(secretPath)
	if err != nil {
		return nil, err
	}

	// BUG FIX #4: TOCTOU — между Stat() выше и реальным чтением файл мог измениться.
	// Повторно читаем mtime ПОСЛЕ чтения файла и сохраняем актуальное значение.
	// Это предотвращает ситуацию когда кэш хранит старый mtime при новых params:
	// если файл снова изменится с тем же mtime (Windows, разрешение ~15ms),
	// Parse вернул бы устаревший кэш вместо перечитывания файла.
	actualMtime := modTime
	if fi2, err2 := os.Stat(secretPath); err2 == nil {
		actualMtime = fi2.ModTime()
	}

	c.mu.Lock()
	c.path = secretPath
	c.mtime = actualMtime
	c.params = params
	c.mu.Unlock()

	return params, nil
}

// Invalidate явно сбрасывает кэш.
// Вызывать сразу после записи нового secret.key чтобы следующий вызов Parse
// гарантированно перечитал файл с диска (mtime может не измениться на Windows).
func (c *VLESSCache) Invalidate() {
	c.mu.Lock()
	c.params = nil
	c.mu.Unlock()
}

// defaultVLESSCache глобальный кэш для обратной совместимости.
// OPT #6: parseVLESSKey читала файл с диска при каждом вызове GenerateSingBoxConfig.
// Кэш инвалидируется только когда mtime файла изменился.
var defaultVLESSCache VLESSCache

// ParseVLESSKeyForTest экспортирует parseVLESSKey для тестов из других пакетов.
// Используется только в _test.go файлах — не вызывать из продакшн-кода.
func ParseVLESSKeyForTest(secretPath string) (*VLESSParams, error) {
	return parseVLESSKey(secretPath)
}

// InvalidateVLESSCache явно сбрасывает глобальный кэш парсера VLESS.
// Вызывать сразу после записи нового secret.key, чтобы следующий вызов
// parseVLESSKey гарантированно перечитал файл с диска, даже если mtime
// на Windows не изменился (разрешение системных часов ~15мс + atomic rename).
func InvalidateVLESSCache() {
	defaultVLESSCache.Invalidate()
}

// parseVLESSKey читает и парсит VLESS URL из файла через глобальный defaultVLESSCache.
func parseVLESSKey(secretPath string) (*VLESSParams, error) {
	return defaultVLESSCache.Parse(secretPath)
}

// readAndParseVLESS выполняет фактическое чтение и парсинг файла.
func readAndParseVLESS(secretPath string) (*VLESSParams, error) {
	content, err := ReadSecretKey(secretPath)
	if err != nil {
		return nil, fmt.Errorf("не удалось прочитать файл '%s': %w", secretPath, err)
	}
	return parseVLESSContentInternal(content)
}

// ReadSecretKey reads secret.key with support for DPAPI-encrypted and legacy plaintext formats.
func ReadSecretKey(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	content := strings.TrimSpace(strings.TrimPrefix(string(data), "\ufeff"))
	if strings.HasPrefix(content, dpapiMagic) {
		enc, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(content, dpapiMagic))
		if err != nil {
			return "", fmt.Errorf("dpapi base64: %w", err)
		}
		plain, err := dpapi.Decrypt(enc)
		if err != nil {
			return "", fmt.Errorf("dpapi decrypt: %w", err)
		}
		return strings.TrimSpace(string(plain)), nil
	}
	return content, nil
}

// WriteSecretKey writes a VLESS URL to secret.key using DPAPI when available.
func WriteSecretKey(path, vlessURL string) error {
	enc, err := dpapi.Encrypt([]byte(vlessURL))
	content := []byte(vlessURL)
	if err == nil {
		content = []byte(dpapiMagic + base64.StdEncoding.EncodeToString(enc))
	}
	return fileutil.WriteAtomic(path, content, 0600)
}

// ParseVLESSContent парсит содержимое VLESS-файла переданное как строка.
// Вынесено отдельно чтобы фазз-тест мог работать без файлового I/O —
// иначе создание temp-файла на каждую итерацию (~3 exec/sec вместо ~60k/sec)
// приводит к зависанию минимизатора и ошибке
// "fuzzing process hung or terminated unexpectedly while minimizing: EOF".
// B-6: экспортировано для использования в handleImportClipboard.
func ParseVLESSContent(content string) (*VLESSParams, error) {
	return parseVLESSContentInternal(content)
}

// parseVLESSContentInternal содержит реальную реализацию парсинга.
func parseVLESSContentInternal(content string) (*VLESSParams, error) {
	// Убираем BOM (U+FEFF) — Блокнот Windows добавляет его при сохранении
	// в UTF-8, что ломает парсинг URL ("first path segment cannot contain colon").
	vlessURL := strings.TrimPrefix(strings.TrimSpace(content), "\ufeff")

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

	// BUG FIX (фаззер): url.Parse нормализует схему к lowercase, поэтому
	// "vlEss://..." проходило проверку parsedURL.Scheme == "vless".
	// Проверяем исходную строку ДО парсинга — только "vless://" (строго строчными).
	if !strings.HasPrefix(vlessURL, "vless://") {
		return nil, fmt.Errorf("ожидается протокол 'vless://', получен другой")
	}

	parsedURL, err := url.Parse(vlessURL)
	if err != nil {
		return nil, fmt.Errorf("неверный формат URL: %w", err)
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
	params := &VLESSParams{
		Address:   parsedURL.Hostname(),
		Port:      port,
		UUID:      parsedURL.User.Username(),
		SNI:       queryParams.Get("sni"),
		PublicKey: queryParams.Get("pbk"),
		ShortID:   queryParams.Get("sid"),
		Flow:      queryParams.Get("flow"),
		Mux:       muxParam == "1" || muxParam == "true",
	}
	fragParam := strings.ToLower(strings.TrimSpace(queryParams.Get("fragment")))
	params.Fragment = fragParam != "0" && fragParam != "false"
	params.FragmentSize = queryParams.Get("fragment_size")
	params.Fingerprint = queryParams.Get("fp")

	// BUG FIX (фаззер): возвращал params с port=0 / port=99999 / пустым Address
	// без ошибки. Теперь fail-fast прямо здесь, до возврата из парсера.
	if params.Address == "" {
		return nil, fmt.Errorf("пустой адрес сервера в URL")
	}
	if params.Port <= 0 || params.Port > 65535 {
		return nil, fmt.Errorf("некорректный порт %d: допустимо 1–65535", params.Port)
	}

	return params, nil
}

func (p *VLESSParams) UTLSFingerprint() string {
	if p != nil && strings.TrimSpace(p.Fingerprint) != "" {
		return strings.TrimSpace(p.Fingerprint)
	}
	return "random"
}

// validateVLESSParams проверяет параметры на корректность.
//
// Поддерживает два режима подключения:
//   - Reality (pbk присутствует в URL): требует SNI, PublicKey, ShortID.
//     Используется с серверами на базе sing-box/xray с XTLS Reality.
//   - Plain TLS (pbk отсутствует): достаточно Address, Port, UUID.
//     Используется с обычными VLESS+TLS серверами (например, v2fly, 3x-ui).
//
// BUG FIX: раньше SNI/PublicKey/ShortID требовались всегда, что ломало
// подключение к новым серверам без Reality — generateSingBoxConfig падал
// с "невалидные параметры: отсутствует SNI", приложение падало на старый
// config.singbox.json, и sing-box крашился с FATAL при попытке открыть
// несуществующий TUN-адаптер.
func validateVLESSParams(params *VLESSParams) error {
	if strings.TrimSpace(params.Address) == "" {
		return fmt.Errorf("отсутствует адрес сервера")
	}
	if params.Port <= 0 || params.Port > 65535 {
		return fmt.Errorf("некорректный порт: %d", params.Port)
	}
	if strings.TrimSpace(params.UUID) == "" {
		return fmt.Errorf("отсутствует UUID")
	}
	// Reality-режим: PublicKey (pbk) присутствует → требуем SNI и ShortID.
	// Plain TLS: PublicKey отсутствует → SNI и ShortID необязательны.
	// Whitespace-only PublicKey ("  ") → явная ошибка: не является ни пустым (plain TLS),
	// ни валидным ключом (Reality). Без этой проверки TrimSpace(" ")="" → plain TLS
	// молча принимался, хотя пользователь явно ввёл некорректное значение.
	trimmedPK := strings.TrimSpace(params.PublicKey)
	if params.PublicKey != "" && trimmedPK == "" {
		return fmt.Errorf("PublicKey содержит только пробелы")
	}
	if trimmedPK != "" {
		if strings.TrimSpace(params.SNI) == "" {
			return fmt.Errorf("отсутствует SNI (обязателен для Reality)")
		}
		if strings.TrimSpace(params.ShortID) == "" {
			return fmt.Errorf("отсутствует ShortID (обязателен для Reality)")
		}
	}
	return nil
}
