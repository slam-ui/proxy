package main

import (
	"encoding/json"
	"fmt"
	"os"
	"proxyclient/internal/config"
)

func main() {
	// Читаем routing.json из текущей директории
	data, err := os.ReadFile("routing.json")
	if err != nil {
		fmt.Printf("Ошибка: не удалось прочитать routing.json: %v\n", err)
		return
	}

	var routing config.RoutingConfig
	if err := json.Unmarshal(data, &routing); err != nil {
		fmt.Printf("Ошибка парсинга routing.json: %v\n", err)
		return
	}

	fmt.Printf("Загружено правил: %d\n", len(routing.Rules))
	fmt.Printf("Default action: %s\n", routing.DefaultAction)

	// Подсчитываем process правила
	var proxyProcs, directProcs, blockProcs int
	for _, rule := range routing.Rules {
		vless := rule.Value
		if rule.Type == config.RuleTypeProcess {
			fmt.Printf("  - Процесс: %s (%s)\n", vless, rule.Action)
			switch rule.Action {
			case config.ActionProxy:
				proxyProcs++
			case config.ActionDirect:
				directProcs++
			case config.ActionBlock:
				blockProcs++
			}
		}
	}

	hasProcessRules := proxyProcs+directProcs+blockProcs > 0
	fmt.Printf("\nПроцесс правила найдено: %d (proxy) + %d (direct) + %d (block) = %v\n",
		proxyProcs, directProcs, blockProcs, hasProcessRules)

	// Читаем config.singbox.json
	cfg_data, err := os.ReadFile("config.singbox.json")
	if err != nil {
		fmt.Printf("Ошибка: не удалось прочитать config.singbox.json: %v\n", err)
		return
	}

	var cfg config.SingBoxConfig
	if err := json.Unmarshal(cfg_data, &cfg); err != nil {
		fmt.Printf("Ошибка парсинга config.singbox.json: %v\n", err)
		return
	}

	fmt.Printf("\nТекущий config.singbox.json:\n")
	fmt.Printf("  FindProcess: %v ⚠️ (должно быть %v)\n", cfg.Route.FindProcess, hasProcessRules)
	fmt.Printf("  AutoDetectInterface: %v ⚠️ (должно быть true)\n", cfg.Route.AutoDetectInterface)
	fmt.Printf("  Final: %s\n", cfg.Route.Final)

	// Проверяем process_name правила в конфиге
	if len(cfg.Route.Rules) > 0 {
		for i, rule := range cfg.Route.Rules {
			if len(rule.ProcessName) > 0 {
				fmt.Printf("  Правило %d: process_name %v -> %s\n", i, rule.ProcessName, rule.Outbound)
			}
		}
	}

	if hasProcessRules && !cfg.Route.FindProcess {
		fmt.Printf("\n🐛 НАЙДЕН БАГ: find_process = false, но есть %d process правил!\n",
			proxyProcs+directProcs+blockProcs)
		fmt.Printf("   Это означает что sing-box не будет детектировать процессы.\n")
	}
}
