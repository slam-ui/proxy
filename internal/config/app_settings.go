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
	StartProxyOnLaunch   bool     `json:"start_proxy_on_launch"`
	ReconnectIntervalMin int      `json:"reconnect_interval_min"`
	KeepaliveEnabled     bool     `json:"keepalive_enabled"`
	KeepaliveIntervalSec int      `json:"keepalive_interval_sec"`
	Schedule             Schedule `json:"schedule"`
	MemoryLimitMB        uint64   `json:"memory_limit_mb"`
	ManualSingBoxConfig  bool     `json:"manual_singbox_config"`
}

func DefaultAppSettings() AppSettings {
	return AppSettings{
		StartProxyOnLaunch:   true,
		KeepaliveEnabled:     true,
		KeepaliveIntervalSec: 120,
	}
}

type Schedule struct {
	Enabled  bool   `json:"enabled"`
	ProxyOn  string `json:"proxy_on"`
	ProxyOff string `json:"proxy_off"`
	Weekdays []int  `json:"weekdays"`
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
		StartProxyOnLaunch   *bool     `json:"start_proxy_on_launch"`
		ReconnectIntervalMin *int      `json:"reconnect_interval_min"`
		KeepaliveEnabled     *bool     `json:"keepalive_enabled"`
		KeepaliveIntervalSec *int      `json:"keepalive_interval_sec"`
		Schedule             *Schedule `json:"schedule"`
		MemoryLimitMB        *uint64   `json:"memory_limit_mb"`
		ManualSingBoxConfig  *bool     `json:"manual_singbox_config"`
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
	if settings.KeepaliveIntervalSec <= 0 {
		settings.KeepaliveIntervalSec = 120
	}
	return settings, nil
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
