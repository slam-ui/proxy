package config

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"

	"proxyclient/internal/fileutil"
)

// RuleType тип правила маршрутизации
type RuleType string

const (
	RuleTypeProcess RuleType = "process"
	RuleTypeDomain  RuleType = "domain"
	RuleTypeIP      RuleType = "ip"
	RuleTypeGeosite RuleType = "geosite"
)

// RuleAction действие
type RuleAction string

const (
	ActionProxy  RuleAction = "proxy"
	ActionDirect RuleAction = "direct"
	ActionBlock  RuleAction = "block"
)

// RoutingRule одно правило маршрутизации
type RoutingRule struct {
	Value  string     `json:"value"`
	Type   RuleType   `json:"type"`
	Action RuleAction `json:"action"`
	Note   string     `json:"note,omitempty"`
}

// B-7: DNSConfig конфигурирует DNS для sing-box.
// RemoteDNS используется для трафика через прокси, DirectDNS для прямого трафика.
// Разрешённые схемы: https://, tls://, udp://, tcp://, quic://
type DNSConfig struct {
	RemoteDNS string `json:"remote_dns"` // Default: "https://1.1.1.1/dns-query"
	DirectDNS string `json:"direct_dns"` // Default: "udp://8.8.8.8"
}

// B-7: DefaultDNSConfig возвращает конфиг DNS по умолчанию
func DefaultDNSConfig() *DNSConfig {
	return &DNSConfig{
		RemoteDNS: "https://1.1.1.1/dns-query",
		DirectDNS: "udp://8.8.8.8",
	}
}

// RoutingConfig конфиг маршрутизации
type RoutingConfig struct {
	DefaultAction RuleAction    `json:"default_action"`
	Rules         []RoutingRule `json:"rules"`
	// BypassEnabled — ручной режим обхода белых списков.
	// Когда true, все пользовательские правила игнорируются и весь трафик
	// (кроме локальных адресов) направляется через прокси.
	// Сохраняется в routing.json, не сбрасывается при перезапуске/TURN-переключении.
	BypassEnabled bool `json:"bypass_enabled,omitempty"`
	// B-7: DNS конфигурация для настраиваемых DNS серверов
	DNS *DNSConfig `json:"dns,omitempty"`
}

func DefaultRoutingConfig() *RoutingConfig {
	return &RoutingConfig{
		DefaultAction: ActionProxy,
		Rules:         []RoutingRule{},
	}
}

func LoadRoutingConfig(path string) (*RoutingConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultRoutingConfig(), nil
		}
		return nil, fmt.Errorf("не удалось прочитать routing config: %w", err)
	}
	var cfg RoutingConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("неверный формат routing config: %w", err)
	}
	SanitizeRoutingConfig(&cfg)
	return &cfg, nil
}

// NormalizeRuleValue канонизирует значение правила:
// убирает URL-схему (https://, http://), query-параметры, fragment, порт и trailing slash.
// Например: "https://2ip.ru/" → "2ip.ru"
//
//	"http://example.com:8080/path?q=1" → "example.com"
//
// Процессы (.exe) и geosite: префиксы не затрагиваются.
func NormalizeRuleValue(val string) string {
	val = strings.TrimSpace(val)

	// BUG FIX (фаззер): нулевые байты (\x00) удаляем ПЕРВЫМИ.
	// Иначе "\xef\x00\xbb\xbf" (BOM с нулём внутри) не матчится TrimPrefix,
	// после удаления нуля собирается в "\xef\xbb\xbf" = BOM — и остаётся.
	val = strings.ReplaceAll(val, "\x00", "")

	// Удаляем BOM (== "\xef\xbb\xbf" == "\ufeff") в цикле до стабилизации.
	// Один проход не достаточен: "\xef\xbb\ufeff\xbf" (BOM внутри BOM-байтов)
	// после первого ReplaceAll даёт "\xef\xbb\xbf" = новый BOM.
	// Цикл гарантирует идемпотентность для любой глубины вложенности.
	for strings.Contains(val, "\ufeff") {
		val = strings.ReplaceAll(val, "\ufeff", "")
	}

	// Проверяем .exe ДО strip комментария.
	//
	// Инвариант фаззера: если raw input заканчивается на .exe, DetectRuleType
	// возвращает process. NormalizeRuleValue тоже должен сохранять этот тип —
	// иначе DetectRuleType(NormalizeRuleValue(input)) даст domain.
	//
	// Для "0#.EXe": raw заканчивается на .exe → ранний return "0#.EXe".
	// DetectRuleType("0#.EXe") = process (видит .exe в конце) ✓
	// Идемпотентность: NormalizeRuleValue("0#.EXe") = "0#.EXe" → повторный вызов тот же ✓
	rawLower := strings.ToLower(val)
	if strings.HasSuffix(rawLower, ".exe") {
		return strings.TrimSpace(val)
	}

	// Strip inline comments (#...) ПЕРЕД остальной обработкой.
	if idx := strings.IndexByte(val, '#'); idx != -1 {
		val = val[:idx]
	}
	val = strings.TrimSpace(val)

	// Убираем пробельные символы в цикле до стабилизации.
	// Проблема: один проход Fields/Join может СОЗДАВАТЬ новые Unicode-пробелы.
	// Пример: "0\xc2 \xa00" — \xc2 и \xa0 разделены пробелом (невалидный UTF-8).
	// После Join они сливаются в U+00A0 (NO-BREAK SPACE) — валидный пробел.
	// Второй вызов NormalizeRuleValue разбивает по U+00A0 → нарушение идемпотентности.
	// Цикл повторяет Fields/Join + BOM-удаление пока строка перестаёт меняться.
	// Гарантированно сходится: каждая итерация либо сокращает строку, либо оставляет её.
	for {
		prev := val
		val = strings.Join(strings.Fields(val), "")
		for strings.Contains(val, "\ufeff") {
			val = strings.ReplaceAll(val, "\ufeff", "")
		}
		if val == prev {
			break
		}
	}

	lower := strings.ToLower(val)
	if strings.HasPrefix(lower, "geosite:") {
		return lower
	}

	val = strings.TrimPrefix(val, "https://")
	val = strings.TrimPrefix(val, "http://")
	val = strings.TrimPrefix(val, "//")

	// Не трогаем CIDR-нотацию (10.0.0.0/8, 2001:db8::/32) —
	// для них слеш является частью значения, а не разделителем пути.
	if idx := strings.IndexByte(val, '/'); idx != -1 && !isIPOrCIDR(val) {
		val = val[:idx]
	}
	if idx := strings.IndexByte(val, '?'); idx != -1 {
		val = val[:idx]
	}
	if !strings.HasPrefix(val, "[") {
		// Strip порта выполняем в цикле до стабильности.
		// ":0:0" → ":0" → "" — каждая итерация удаляет один числовой суффикс после ':'.
		for {
			idx := strings.LastIndexByte(val, ':')
			if idx < 0 {
				break
			}
			host := val[:idx]
			port := val[idx+1:]
			isPort := len(port) > 0
			for _, c := range port {
				if c < '0' || c > '9' {
					isPort = false
					break
				}
			}
			if !isPort {
				break
			}
			val = host
		}
	}

	return strings.ToLower(strings.TrimSpace(val))
}

// SanitizeRoutingConfig исправляет неверно классифицированные правила и
// нормализует значения (убирает URL-схемы, порты, пути из domain-правил).
// Вызывается при загрузке — автоматически мигрирует старые правила.
func SanitizeRoutingConfig(cfg *RoutingConfig) {
	validTypes := map[RuleType]bool{
		RuleTypeProcess: true, RuleTypeDomain: true,
		RuleTypeIP: true, RuleTypeGeosite: true,
	}
	for i := range cfg.Rules {
		rule := &cfg.Rules[i]

		// BUG FIX (фаззер): определяем тип ДО нормализации, потому что
		// нормализация может уничтожить признак типа.
		// Пример: "0#.EXe" → NormalizeRuleValue → "0" (комментарий обрезан),
		// DetectRuleType("0") = domain, хотя исходное значение было process.
		// Решение: детектим на оригинале, нормализуем значение, тип берём из детекта.
		detectedFromOriginal := DetectRuleType(rule.Value)

		normalized := NormalizeRuleValue(rule.Value)
		if normalized != "" {
			rule.Value = normalized
		}

		// BUG FIX (фаззер): ранее исправлялся только пустой тип ("").
		// Невалидный тип ("unknown", произвольная строка) оставался нетронутым —
		// sing-box не мог маршрутизировать такое правило.
		// Теперь: любой не-валидный тип перезаписывается автодетектом от оригинала.
		if !validTypes[rule.Type] {
			rule.Type = detectedFromOriginal
		}
		// Специальный случай: geosite-правило классифицировано как process (редко,
		// но возможно при старых форматах данных)
		if rule.Type == RuleTypeGeosite && detectedFromOriginal == RuleTypeProcess {
			rule.Type = RuleTypeProcess
		}
	}
}

// SaveRoutingConfig атомарно сохраняет конфиг: пишет во временный файл,
// затем переименовывает — защита от порчи при аварийном завершении
func SaveRoutingConfig(path string, cfg *RoutingConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := fileutil.WriteAtomic(path, data, 0644); err != nil {
		return fmt.Errorf("не удалось сохранить routing config: %w", err)
	}
	return nil
}

// DetectRuleType автоматически определяет тип правила по значению.
// Работает с raw значением (без strip комментариев) — инвариант фаззера:
// если TrimSpace(ToLower(input)) заканчивается на ".exe", должен вернуть process.
// NormalizeRuleValue тоже проверяет .exe до strip комментариев, поэтому
// DetectRuleType(input) == DetectRuleType(NormalizeRuleValue(input)) для process/geosite.
func DetectRuleType(value string) RuleType {
	v := strings.ToLower(strings.TrimSpace(value))
	if strings.HasSuffix(v, ".exe") {
		return RuleTypeProcess
	}
	if strings.HasPrefix(v, "geosite:") {
		return RuleTypeGeosite
	}
	if isIPOrCIDR(v) {
		return RuleTypeIP
	}
	return RuleTypeDomain
}

func isIPOrCIDR(s string) bool {
	// Защита от DoS: IP адреса никогда не длинней ~45 символов (IPv6 адрес).
	// CIDR: ~50 символов максимум. Более длинные строки точно не IP/CIDR.
	if len(s) > 100 {
		return false
	}

	// CIDR нотация
	if strings.Contains(s, "/") {
		_, _, err := net.ParseCIDR(s)
		return err == nil
	}
	// Просто IP
	return net.ParseIP(s) != nil
}
