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
	StartProxyOnLaunch bool `json:"start_proxy_on_launch"`
}

func DefaultAppSettings() AppSettings {
	return AppSettings{
		StartProxyOnLaunch: true,
	}
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
		StartProxyOnLaunch *bool `json:"start_proxy_on_launch"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return settings, fmt.Errorf("неверный формат настроек: %w", err)
	}
	if raw.StartProxyOnLaunch != nil {
		settings.StartProxyOnLaunch = *raw.StartProxyOnLaunch
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
