package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// RuleType тип правила маршрутизации
type RuleType string

const (
	RuleTypeProcess RuleType = "process" // chrome.exe
	RuleTypeDomain  RuleType = "domain"  // google.com, .google.com
	RuleTypeIP      RuleType = "ip"      // 8.8.8.8, 192.168.0.0/24
	RuleTypeGeosite RuleType = "geosite" // geosite:discord
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
	Type   RuleType   `json:"type"`   // process / domain / ip
	Action RuleAction `json:"action"` // proxy / direct / block
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
	return &cfg, nil
}

func SaveRoutingConfig(path string, cfg *RoutingConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// DetectRuleType автоматически определяет тип правила по значению
func DetectRuleType(value string) RuleType {
	v := strings.ToLower(strings.TrimSpace(value))
	if strings.HasSuffix(v, ".exe") {
		return RuleTypeProcess
	}
	if isIPOrCIDR(v) {
		return RuleTypeIP
	}
	if strings.HasPrefix(v, "geosite:") {
		return RuleTypeGeosite
	}
	return RuleTypeDomain
}

func isIPOrCIDR(s string) bool {
	if strings.Contains(s, "/") {
		return true
	}
	if strings.Contains(s, ":") {
		return true // IPv6
	}
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if len(p) == 0 || len(p) > 3 {
			return false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

// ── Обратная совместимость со старым кодом ────────────────

type ProcessAction = RuleAction

const (
	ProcessProxy  = ActionProxy
	ProcessDirect = ActionDirect
	ProcessBlock  = ActionBlock
)

type ProcessRule struct {
	ProcessName string        `json:"process_name"`
	Action      ProcessAction `json:"action"`
}

type TunConfig struct {
	Enabled      bool          `json:"enabled"`
	ProcessRules []ProcessRule `json:"process_rules"`
}

func LoadTunConfig(path string) (*TunConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &TunConfig{Enabled: false}, nil
		}
		return nil, err
	}
	var cfg TunConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func SaveTunConfig(path string, cfg *TunConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func GenerateRuntimeConfigWithTun(templatePath, secretPath, outputPath string, tunCfg *TunConfig) error {
	return GenerateRuntimeConfig(templatePath, secretPath, outputPath)
}

// GenerateRuntimeConfigWithRouting — старый XRay генератор, оставлен для совместимости
func GenerateRuntimeConfigWithRouting(templatePath, secretPath, outputPath string, routingCfg *RoutingConfig) error {
	return GenerateRuntimeConfig(templatePath, secretPath, outputPath)
}
