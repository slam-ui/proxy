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
	"proxyclient/internal/logger"
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
	Encryption   string
	Security     string
	ALPN         []string
	Insecure     bool
	Type         string
	HeaderType   string
	Path         string
	Host         []string
	ServiceName  string
	GRPCMode     string
	EarlyData    int
}

var (
	vlessLoggerMu sync.Mutex
	vlessLogger   logger.Logger = logger.New(logger.Config{Level: logger.WarnLevel})
)

func warnVLESS(format string, args ...interface{}) {
	vlessLoggerMu.Lock()
	log := vlessLogger
	vlessLoggerMu.Unlock()
	if log != nil {
		log.Warn("vless: "+format, args...)
	}
}

func setVLESSLoggerForTest(log logger.Logger) func() {
	vlessLoggerMu.Lock()
	prev := vlessLogger
	vlessLogger = log
	vlessLoggerMu.Unlock()
	return func() {
		vlessLoggerMu.Lock()
		vlessLogger = prev
		vlessLoggerMu.Unlock()
	}
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
		defer zeroBytes(plain)
		return strings.TrimSpace(string(plain)), nil
	}
	return content, nil
}

// WriteSecretKey writes a VLESS URL to secret.key using DPAPI when available.
func WriteSecretKey(path, vlessURL string) error {
	plain := []byte(vlessURL)
	defer zeroBytes(plain)

	enc, err := dpapi.Encrypt(plain)
	content := append([]byte(nil), plain...)
	defer zeroBytes(content)
	if err == nil {
		content = []byte(dpapiMagic + base64.StdEncoding.EncodeToString(enc))
	}
	return fileutil.WriteAtomic(path, content, 0600)
}

func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
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

func queryFirst(values url.Values, names ...string) string {
	for _, name := range names {
		for _, value := range values[name] {
			if value != "" {
				return value
			}
		}
	}
	return ""
}

func splitQueryList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func parseBoolQuery(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true":
		return true
	default:
		return false
	}
}

func parseALPN(value string) []string {
	alpn := splitQueryList(value)
	for _, proto := range alpn {
		if strings.EqualFold(proto, "h3") {
			warnVLESS("alpn=h3 не поддерживается sing-box VLESS, использую h2,http/1.1")
			return nil
		}
	}
	return alpn
}

func parseWSPath(path string) (string, int, error) {
	if path == "" {
		return "/", 0, nil
	}
	base, rawQuery, hasQuery := strings.Cut(path, "?")
	if !hasQuery {
		if base == "" {
			return "/", 0, nil
		}
		return base, 0, nil
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", 0, fmt.Errorf("ошибка парсинга VLESS URL: параметр path содержит некорректный query: %w", err)
	}
	earlyData := 0
	if ed := strings.TrimSpace(values.Get("ed")); ed != "" {
		n, err := strconv.Atoi(ed)
		if err != nil || n <= 0 {
			return "", 0, fmt.Errorf("ошибка парсинга VLESS URL: ws path содержит ed=%q, ожидается положительное число", ed)
		}
		earlyData = n
	}
	if base == "" {
		base = "/"
	}
	return base, earlyData, nil
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

	encryption := strings.ToLower(strings.TrimSpace(queryParams.Get("encryption")))
	if encryption == "" {
		encryption = "none"
	}
	if encryption != "none" {
		return nil, fmt.Errorf("ошибка парсинга VLESS URL: encryption=%s не поддерживается; ожидается encryption=none", encryption)
	}

	security := strings.ToLower(strings.TrimSpace(queryParams.Get("security")))
	publicKey := queryFirst(queryParams, "pbk", "publicKey", "publickey")
	if security == "" {
		if strings.TrimSpace(publicKey) != "" {
			security = "reality"
		} else {
			security = "tls"
		}
	}
	switch security {
	case "none", "tls", "reality":
	default:
		return nil, fmt.Errorf("ошибка парсинга VLESS URL: security=%s не поддерживается; ожидается none, tls или reality", security)
	}
	if strings.TrimSpace(publicKey) != "" && security != "reality" {
		warnVLESS("pbk задан, но security=%s; использую reality", security)
		security = "reality"
	}
	if security == "reality" && strings.TrimSpace(publicKey) == "" {
		return nil, fmt.Errorf("ошибка парсинга VLESS URL: security=reality требует параметр pbk")
	}
	if spx := strings.TrimSpace(queryParams.Get("spx")); spx != "" {
		warnVLESS("параметр spx не поддерживается sing-box и будет проигнорирован")
	}

	transportType := strings.ToLower(strings.TrimSpace(queryParams.Get("type")))
	if transportType == "" {
		transportType = "tcp"
	}
	if transportType == "h2" {
		transportType = "http"
	}
	switch transportType {
	case "tcp", "ws", "grpc", "http", "httpupgrade":
	case "quic", "kcp", "mkcp":
		return nil, fmt.Errorf("ошибка парсинга VLESS URL: транспорт type=%s не поддерживается sing-box; поддерживаемые: tcp, ws, grpc, http, httpupgrade", transportType)
	default:
		return nil, fmt.Errorf("ошибка парсинга VLESS URL: транспорт type=%s не поддерживается; поддерживаемые: tcp, ws, grpc, http, httpupgrade", transportType)
	}

	headerType := strings.ToLower(strings.TrimSpace(queryParams.Get("headerType")))
	if headerType == "" {
		headerType = "none"
	}
	if headerType != "none" && headerType != "http" {
		return nil, fmt.Errorf("ошибка парсинга VLESS URL: headerType=%s не поддерживается; ожидается none или http", headerType)
	}

	rawPath := queryParams.Get("path")
	pathValue := rawPath
	earlyData := 0
	if transportType == "ws" {
		var err error
		pathValue, earlyData, err = parseWSPath(rawPath)
		if err != nil {
			return nil, err
		}
	} else if (transportType == "httpupgrade" || transportType == "tcp" && headerType == "http") && pathValue == "" {
		pathValue = "/"
	}
	if transportType == "http" && security == "none" {
		return nil, fmt.Errorf("ошибка парсинга VLESS URL: транспорт type=http требует TLS и alpn=h2; security=none недопустим")
	}

	hostParam := queryParams.Get("host")
	var hosts []string
	switch transportType {
	case "http":
		hosts = splitQueryList(hostParam)
	case "ws", "httpupgrade":
		if host := strings.TrimSpace(hostParam); host != "" {
			hosts = []string{host}
		}
	case "tcp":
		if headerType == "http" {
			hosts = splitQueryList(hostParam)
		}
	}

	serviceName := queryFirst(queryParams, "serviceName", "service_name")
	grpcMode := strings.ToLower(strings.TrimSpace(queryParams.Get("mode")))
	if transportType == "grpc" {
		if strings.TrimSpace(serviceName) == "" {
			return nil, fmt.Errorf("ошибка парсинга VLESS URL: транспорт type=grpc требует параметр serviceName")
		}
		if grpcMode != "" && grpcMode != "gun" {
			warnVLESS("mode=%s проигнорирован, sing-box gRPC использует gun", grpcMode)
		}
	}

	alpn := parseALPN(queryParams.Get("alpn"))
	insecure := parseBoolQuery(queryParams.Get("allowInsecure")) || parseBoolQuery(queryParams.Get("insecure"))
	if insecure {
		warnVLESS("insecure=1 - TLS-проверка сертификата отключена")
	}

	muxParam := queryParams.Get("mux")
	if muxParam == "" {
		muxParam = queryParams.Get("multiplex")
	}
	params := &VLESSParams{
		Address:     parsedURL.Hostname(),
		Port:        port,
		UUID:        parsedURL.User.Username(),
		SNI:         queryFirst(queryParams, "sni", "serverName", "servername", "peer"),
		PublicKey:   publicKey,
		ShortID:     queryFirst(queryParams, "sid", "shortId", "shortid"),
		Flow:        queryParams.Get("flow"),
		Mux:         muxParam == "1" || muxParam == "true",
		Encryption:  encryption,
		Security:    security,
		ALPN:        alpn,
		Insecure:    insecure,
		Type:        transportType,
		HeaderType:  headerType,
		Path:        pathValue,
		Host:        hosts,
		ServiceName: serviceName,
		GRPCMode:    grpcMode,
		EarlyData:   earlyData,
	}
	fragParam := strings.ToLower(strings.TrimSpace(queryParams.Get("fragment")))
	params.Fragment = fragParam != "0" && fragParam != "false"
	params.FragmentSize = queryParams.Get("fragment_size")
	params.Fingerprint = queryFirst(queryParams, "fp", "fingerprint", "utls")

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
