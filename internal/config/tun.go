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

// RoutingConfig конфиг маршрутизации
type RoutingConfig struct {
	DefaultAction RuleAction    `json:"default_action"`
	Rules         []RoutingRule `json:"rules"`
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
//           "http://example.com:8080/path?q=1" → "example.com"
// Процессы (.exe) и geosite: префиксы не затрагиваются.
func NormalizeRuleValue(val string) string {
	val = strings.TrimSpace(val)
	val = strings.TrimPrefix(val, "\xef\xbb\xbf")

	lower := strings.ToLower(val)
	if strings.HasSuffix(lower, ".exe") {
		// Process names: сохраняем оригинальный регистр — sing-box матчит с учётом регистра
		return strings.TrimSpace(val)
	}
	if strings.HasPrefix(lower, "geosite:") {
		return lower
	}

	val = strings.TrimPrefix(val, "https://")
	val = strings.TrimPrefix(val, "http://")
	val = strings.TrimPrefix(val, "//")

	if idx := strings.IndexByte(val, '/'); idx != -1 {
		val = val[:idx]
	}
	if idx := strings.IndexAny(val, "?#"); idx != -1 {
		val = val[:idx]
	}
	if !strings.HasPrefix(val, "[") {
		if idx := strings.LastIndexByte(val, ':'); idx != -1 {
			host := val[:idx]
			port := val[idx+1:]
			isPort := len(port) > 0
			for _, c := range port {
				if c < '0' || c > '9' {
					isPort = false
					break
				}
			}
			if isPort {
				val = host
			}
		}
	}

	return strings.ToLower(strings.TrimSpace(val))
}

// SanitizeRoutingConfig исправляет неверно классифицированные правила и
// нормализует значения (убирает URL-схемы, порты, пути из domain-правил).
// Вызывается при загрузке — автоматически мигрирует старые правила.
func SanitizeRoutingConfig(cfg *RoutingConfig) {
	for i := range cfg.Rules {
		rule := &cfg.Rules[i]

		normalized := NormalizeRuleValue(rule.Value)
		if normalized != "" {
			rule.Value = normalized
		}

		detected := DetectRuleType(rule.Value)
		if rule.Type == RuleTypeGeosite && detected == RuleTypeProcess {
			rule.Type = RuleTypeProcess
		}
		if rule.Type == "" {
			rule.Type = detected
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

// DetectRuleType автоматически определяет тип правила по значению
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
	// CIDR нотация
	if strings.Contains(s, "/") {
		_, _, err := net.ParseCIDR(s)
		return err == nil
	}
	// Просто IP
	return net.ParseIP(s) != nil
}
