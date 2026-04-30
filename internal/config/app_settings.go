package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"proxyclient/internal/fileutil"
)

const AppSettingsFile = DataDir + "/settings.json"

type AppSettings struct {
	StartProxyOnLaunch   bool                      `json:"start_proxy_on_launch"`
	ReconnectIntervalMin int                       `json:"reconnect_interval_min"`
	KeepaliveEnabled     bool                      `json:"keepalive_enabled"`
	KeepaliveIntervalSec int                       `json:"keepalive_interval_sec"`
	Schedule             Schedule                  `json:"schedule"`
	MemoryLimitMB        uint64                    `json:"memory_limit_mb"`
	ManualSingBoxConfig  bool                      `json:"manual_singbox_config"`
	SmartFailover        SmartFailoverSettings     `json:"smart_failover"`
	DNSGuard             DNSGuardSettings          `json:"dns_guard"`
	NetworkProtection    NetworkProtectionSettings `json:"network_protection"`
	TrafficBudget        TrafficBudgetSettings     `json:"traffic_budget"`
}

func DefaultAppSettings() AppSettings {
	return AppSettings{
		StartProxyOnLaunch:   true,
		KeepaliveEnabled:     true,
		KeepaliveIntervalSec: 120,
		SmartFailover: SmartFailoverSettings{
			MaxLatencyMs:     800,
			CheckIntervalSec: 60,
			MinImprovementMs: 50,
		},
		DNSGuard: DNSGuardSettings{
			Mode:             "warn",
			CheckIntervalSec: 60,
		},
		NetworkProtection: NetworkProtectionSettings{
			Enabled:          true,
			CheckIntervalSec: 10,
		},
		TrafficBudget: TrafficBudgetSettings{
			WarnPercent: 80,
		},
	}
}

type Schedule struct {
	Enabled  bool   `json:"enabled"`
	ProxyOn  string `json:"proxy_on"`
	ProxyOff string `json:"proxy_off"`
	Weekdays []int  `json:"weekdays"`
}

type SmartFailoverSettings struct {
	Enabled          bool `json:"enabled"`
	MaxLatencyMs     int  `json:"max_latency_ms"`
	CheckIntervalSec int  `json:"check_interval_sec"`
	MinImprovementMs int  `json:"min_improvement_ms"`
}

type DNSGuardSettings struct {
	Enabled          bool   `json:"enabled"`
	Mode             string `json:"mode"`
	CheckIntervalSec int    `json:"check_interval_sec"`
}

type NetworkProtectionSettings struct {
	Enabled          bool `json:"enabled"`
	StrictOnChange   bool `json:"strict_on_change"`
	CheckIntervalSec int  `json:"check_interval_sec"`
}

type TrafficBudgetSettings struct {
	Enabled           bool  `json:"enabled"`
	SessionLimitMB    int64 `json:"session_limit_mb"`
	TotalLimitMB      int64 `json:"total_limit_mb"`
	WarnPercent       int   `json:"warn_percent"`
	BlockWhenExceeded bool  `json:"block_when_exceeded"`
}

func LoadAppSettings(path string) (AppSettings, error) {
	settings := DefaultAppSettings()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return settings, nil
		}
		return settings, fmt.Errorf("не удалось прочитать настройки: %w", err)
	}

	var raw struct {
		StartProxyOnLaunch   *bool                      `json:"start_proxy_on_launch"`
		ReconnectIntervalMin *int                       `json:"reconnect_interval_min"`
		KeepaliveEnabled     *bool                      `json:"keepalive_enabled"`
		KeepaliveIntervalSec *int                       `json:"keepalive_interval_sec"`
		Schedule             *Schedule                  `json:"schedule"`
		MemoryLimitMB        *uint64                    `json:"memory_limit_mb"`
		ManualSingBoxConfig  *bool                      `json:"manual_singbox_config"`
		SmartFailover        *SmartFailoverSettings     `json:"smart_failover"`
		DNSGuard             *DNSGuardSettings          `json:"dns_guard"`
		NetworkProtection    *NetworkProtectionSettings `json:"network_protection"`
		TrafficBudget        *TrafficBudgetSettings     `json:"traffic_budget"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return settings, fmt.Errorf("неверный формат настроек: %w", err)
	}
	if raw.StartProxyOnLaunch != nil {
		settings.StartProxyOnLaunch = *raw.StartProxyOnLaunch
	}
	if raw.ReconnectIntervalMin != nil {
		settings.ReconnectIntervalMin = *raw.ReconnectIntervalMin
	}
	if raw.KeepaliveEnabled != nil {
		settings.KeepaliveEnabled = *raw.KeepaliveEnabled
	}
	if raw.KeepaliveIntervalSec != nil {
		settings.KeepaliveIntervalSec = *raw.KeepaliveIntervalSec
	}
	if raw.Schedule != nil {
		settings.Schedule = *raw.Schedule
	}
	if raw.MemoryLimitMB != nil {
		settings.MemoryLimitMB = *raw.MemoryLimitMB
	}
	if raw.ManualSingBoxConfig != nil {
		settings.ManualSingBoxConfig = *raw.ManualSingBoxConfig
	}
	if raw.SmartFailover != nil {
		settings.SmartFailover = *raw.SmartFailover
	}
	if raw.DNSGuard != nil {
		settings.DNSGuard = *raw.DNSGuard
	}
	if raw.NetworkProtection != nil {
		settings.NetworkProtection = *raw.NetworkProtection
	}
	if raw.TrafficBudget != nil {
		settings.TrafficBudget = *raw.TrafficBudget
	}
	if settings.KeepaliveIntervalSec <= 0 {
		settings.KeepaliveIntervalSec = 120
	}
	normalizeAppSettings(&settings)
	return settings, nil
}

func normalizeAppSettings(settings *AppSettings) {
	if settings.SmartFailover.MaxLatencyMs <= 0 {
		settings.SmartFailover.MaxLatencyMs = 800
	}
	if settings.SmartFailover.CheckIntervalSec < 15 {
		settings.SmartFailover.CheckIntervalSec = 60
	}
	if settings.SmartFailover.MinImprovementMs < 0 {
		settings.SmartFailover.MinImprovementMs = 50
	}
	if settings.DNSGuard.Mode != "strict" {
		settings.DNSGuard.Mode = "warn"
	}
	if settings.DNSGuard.CheckIntervalSec < 15 {
		settings.DNSGuard.CheckIntervalSec = 60
	}
	if settings.NetworkProtection.CheckIntervalSec < 5 {
		settings.NetworkProtection.CheckIntervalSec = 10
	}
	if settings.TrafficBudget.WarnPercent <= 0 || settings.TrafficBudget.WarnPercent > 100 {
		settings.TrafficBudget.WarnPercent = 80
	}
}

func SaveAppSettings(path string, settings AppSettings) error {
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("не удалось создать папку настроек: %w", err)
	}
	if err := fileutil.WriteAtomic(path, data, 0644); err != nil {
		return fmt.Errorf("не удалось сохранить настройки: %w", err)
	}
	return nil
}
