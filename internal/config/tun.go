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
	return &cfg, nil
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
