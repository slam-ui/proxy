package config

import (
	"encoding/json"
	"os"
	"testing"
)

// TestFindProcessGeneration проверяет генерируется ли find_process в конфиге
func TestFindProcessGeneration(t *testing.T) {
	// Создаём тестовый routing config с process правилами
	routingCfg := &RoutingConfig{
		DefaultAction: ActionDirect,
		Rules: []RoutingRule{
			{Value: "discord.exe", Type: RuleTypeProcess, Action: ActionProxy},
			{Value: "code.exe", Type: RuleTypeProcess, Action: ActionProxy},
			{Value: "example.com", Type: RuleTypeDomain, Action: ActionProxy},
		},
	}

	// Генерируем временные файлы
	tmpSecret := t.TempDir() + "/secret.key"
	tmpConfig := t.TempDir() + "/config.json"

	// Пишем тестовый VLESS ключ
	vlessKey := "vless://5bbf74e6-38ce-46d6-9279-376320662d55@46.22.211.24:29528?encryption=none&security=reality&sni=yahoo.com&fp=random&pbk=o2O8x21jRV5jCayKz_9j1emxzZZRVQ4YaA64zPaVnQc&sid=cb"
	if err := os.WriteFile(tmpSecret, []byte(vlessKey), 0644); err != nil {
		t.Fatalf("Не удалось написать секретный ключ: %v", err)
	}

	// Генерируем конфиг
	if err := GenerateSingBoxConfig(tmpSecret, tmpConfig, routingCfg); err != nil {
		t.Fatalf("Генерация конфига провалена: %v", err)
	}

	// Читаем сгенерированный конфиг
	data, err := os.ReadFile(tmpConfig)
	if err != nil {
		t.Fatalf("Не удалось прочитать генерированный конфиг: %v", err)
	}

	// Парсим JSON
	var cfg SingBoxConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("Не удалось распарсить JSON: %v", err)
	}

	// Проверяем find_process
	if !cfg.Route.FindProcess {
		t.Errorf("BUG: find_process = %v, ожидается true (есть process правила)",
			cfg.Route.FindProcess)
		t.Logf("Route config: %v", cfg.Route)
	} else {
		t.Logf("✓ find_process корректно установлен на true")
	}

	// Проверяем что есть process_name правила
	hasProcessRules := false
	for _, rule := range cfg.Route.Rules {
		if len(rule.ProcessName) > 0 {
			hasProcessRules = true
			t.Logf("Найдено процесс правило: %v", rule.ProcessName)
		}
	}

	if hasProcessRules && !cfg.Route.FindProcess {
		t.Error("КРИТИЧЕСКИЙ БАГ: есть process_name правила, но find_process = false!")
	}

	// Выводим raw JSON для отладки
	t.Logf("Route section JSON:\n%s", string(data[len(data)-500:]))
}
