package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// ProcessAction действие для процесса
type ProcessAction string

const (
	ProcessProxy  ProcessAction = "proxy"
	ProcessDirect ProcessAction = "direct"
	ProcessBlock  ProcessAction = "block"
)

// ProcessRule правило для конкретного процесса
type ProcessRule struct {
	ProcessName string        `json:"process_name"` // "firefox.exe"
	Action      ProcessAction `json:"action"`       // proxy / direct / block
}

// TunConfig настройки TUN режима
type TunConfig struct {
	Enabled      bool          `json:"enabled"`
	ProcessRules []ProcessRule `json:"process_rules"`
}

// xrayRoutingRule внутренняя структура правила роутинга XRay
type xrayRoutingRule struct {
	Type        string   `json:"type"`
	ProcessName []string `json:"process_name,omitempty"`
	InboundTag  []string `json:"inboundTag,omitempty"`
	OutboundTag string   `json:"outboundTag"`
	Domain      []string `json:"domain,omitempty"`
	IP          []string `json:"ip,omitempty"`
}

// LoadTunConfig загружает TUN конфиг из файла
func LoadTunConfig(path string) (*TunConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &TunConfig{Enabled: false}, nil
		}
		return nil, fmt.Errorf("не удалось прочитать tun config: %w", err)
	}

	var cfg TunConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("неверный формат tun config: %w", err)
	}

	return &cfg, nil
}

// SaveTunConfig сохраняет TUN конфиг в файл
func SaveTunConfig(path string, cfg *TunConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("ошибка сериализации tun config: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("не удалось сохранить tun config: %w", err)
	}
	return nil
}

// GenerateRuntimeConfigWithTun генерирует конфиг XRay с TUN правилами по процессам
func GenerateRuntimeConfigWithTun(templatePath, secretPath, outputPath string, tunCfg *TunConfig) error {
	// 1. Парсим VLESS ключ
	vlessParams, err := parseVLESSKey(secretPath)
	if err != nil {
		return fmt.Errorf("ошибка парсинга VLESS ключа: %w", err)
	}

	if err := validateVLESSParams(vlessParams); err != nil {
		return fmt.Errorf("невалидные параметры VLESS: %w", err)
	}

	// 2. Загружаем шаблон
	cfg, err := loadTemplate(templatePath)
	if err != nil {
		return fmt.Errorf("ошибка загрузки шаблона: %w", err)
	}

	// 3. Подставляем VLESS параметры
	if err := updateConfig(cfg, vlessParams); err != nil {
		return fmt.Errorf("ошибка обновления конфигурации: %w", err)
	}

	// 4. Применяем TUN правила если включены
	if tunCfg != nil && tunCfg.Enabled {
		if err := applyProcessRules(cfg, tunCfg.ProcessRules); err != nil {
			return fmt.Errorf("ошибка применения process rules: %w", err)
		}
	}

	// 5. Сохраняем
	return saveConfig(cfg, outputPath)
}

// applyProcessRules добавляет правила роутинга по процессам в конфиг XRay
func applyProcessRules(cfg map[string]interface{}, rules []ProcessRule) error {
	routing, ok := cfg["routing"].(map[string]interface{})
	if !ok {
		routing = map[string]interface{}{"domainStrategy": "IPIfNonMatch"}
		cfg["routing"] = routing
	}

	existingRules, _ := routing["rules"].([]interface{})

	// Группируем процессы по действию
	proxyProcesses := []string{}
	directProcesses := []string{}
	blockProcesses := []string{}

	for _, r := range rules {
		switch r.Action {
		case ProcessProxy:
			proxyProcesses = append(proxyProcesses, r.ProcessName)
		case ProcessDirect:
			directProcesses = append(directProcesses, r.ProcessName)
		case ProcessBlock:
			blockProcesses = append(blockProcesses, r.ProcessName)
		}
	}

	// Строим новые правила (process rules идут ПЕРВЫМИ — выше приоритет)
	newRules := []interface{}{}

	if len(blockProcesses) > 0 {
		newRules = append(newRules, map[string]interface{}{
			"type":         "field",
			"process_name": blockProcesses,
			"outboundTag":  "block",
		})
	}

	if len(directProcesses) > 0 {
		newRules = append(newRules, map[string]interface{}{
			"type":         "field",
			"process_name": directProcesses,
			"outboundTag":  "direct",
		})
	}

	if len(proxyProcesses) > 0 {
		newRules = append(newRules, map[string]interface{}{
			"type":         "field",
			"process_name": proxyProcesses,
			"outboundTag":  "proxy-out",
		})
	}

	// Добавляем старые правила после process rules
	newRules = append(newRules, existingRules...)
	routing["rules"] = newRules

	return nil
}
