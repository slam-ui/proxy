package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"proxyclient/internal/api"
	"proxyclient/internal/apprules"
	"proxyclient/internal/config"
	"proxyclient/internal/logger"
	"proxyclient/internal/process"
	"proxyclient/internal/proxy"
	"proxyclient/internal/xray"
)

const (
	templateFile     = "config.template.json"
	secretFile       = "secret.key"
	runtimeFile      = "config.runtime.json"
	appRulesFile     = "app_rules.json"
	tunConfigFile    = "tun_rules.json"
	xrayExecutable   = "./xray_core/xray.exe"
	apiListenAddress = ":8080"
	shutdownTimeout  = 10 * time.Second
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("Приложение завершилось с ошибкой: %v", err)
	}
}

func run() error {
	appLogger := logger.New(logger.Config{
		Level:  logger.InfoLevel,
		Output: os.Stdout,
	})

	appLogger.Info("Запуск прокси-клиента...")

	// 1. Загружаем TUN правила (если есть)
	tunCfg, err := config.LoadTunConfig(tunConfigFile)
	if err != nil {
		appLogger.Warn("Не удалось загрузить tun_rules.json, используем базовый режим: %v", err)
		tunCfg = &config.TunConfig{Enabled: false}
	}

	if tunCfg.Enabled {
		appLogger.Info("TUN режим включён, правил процессов: %d", len(tunCfg.ProcessRules))
	}

	// 2. Генерируем конфигурацию XRay (с TUN правилами если включены)
	appLogger.Info("Генерация конфигурации...")
	if err := config.GenerateRuntimeConfigWithTun(templateFile, secretFile, runtimeFile, tunCfg); err != nil {
		return fmt.Errorf("не удалось сгенерировать конфигурацию: %w", err)
	}
	appLogger.Info("Конфигурация успешно сгенерирована")

	// 3. Создаём прокси-менеджер
	proxyManager := proxy.NewManager(appLogger)

	// 4. Включаем системный прокси
	appLogger.Info("Включение системного прокси...")
	proxyConfig := proxy.Config{
		Address:  "127.0.0.1:10807",
		Override: "<local>",
	}
	if err := proxyManager.Enable(proxyConfig); err != nil {
		appLogger.Warn("Не удалось включить системный прокси: %v", err)
	} else {
		appLogger.Info("Системный прокси включён: %s", proxyConfig.Address)
	}

	// 5. Запускаем XRay
	appLogger.Info("Запуск XRay...")
	xrayManager, err := xray.NewManager(xray.Config{
		ExecutablePath: xrayExecutable,
		ConfigPath:     runtimeFile,
		Logger:         appLogger,
	})
	if err != nil {
		_ = proxyManager.Disable()
		return fmt.Errorf("не удалось запустить XRay: %w", err)
	}
	appLogger.Info("XRay запущен успешно")

	// 6. Initialize per-app proxy components
	appLogger.Info("Инициализация per-app proxy...")

	storage := apprules.NewFileStorage(appRulesFile)
	rulesEngine, err := apprules.NewPersistentEngine(storage)
	if err != nil {
		appLogger.Warn("Failed to initialize rules engine: %v", err)
		rulesEngine = nil
	} else {
		rules := rulesEngine.ListRules()
		appLogger.Info("Rules engine initialized with %d rules", len(rules))
	}

	var processMonitor process.Monitor
	var processLauncher process.Launcher

	if rulesEngine != nil {
		processMonitor = process.NewMonitor(appLogger)
		if err := processMonitor.Start(); err != nil {
			appLogger.Warn("Failed to start process monitor: %v", err)
		} else {
			appLogger.Info("Process monitor started")
		}

		processLauncher = process.NewLauncher(appLogger, rulesEngine)
		appLogger.Info("Per-app proxy initialized successfully")
	}

	// 7. Запускаем HTTP API
	apiServer := api.NewServer(api.Config{
		ListenAddress: apiListenAddress,
		XRayManager:   xrayManager,
		ProxyManager:  proxyManager,
		ConfigPath:    runtimeFile,
		Logger:        appLogger,
		TunConfig:     tunCfg,
		TunConfigPath: tunConfigFile,
		TemplatePath:  templateFile,
		SecretPath:    secretFile,
		RuntimePath:   runtimeFile,
	})

	if rulesEngine != nil && processMonitor != nil && processLauncher != nil {
		apiServer.SetupAppProxyRoutes(rulesEngine, processMonitor, processLauncher)
		appLogger.Info("Per-app proxy API routes registered")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	apiErrChan := make(chan error, 1)
	go func() {
		appLogger.Info("API сервер запущен на %s", apiListenAddress)
		if startErr := apiServer.Start(ctx); startErr != nil {
			apiErrChan <- fmt.Errorf("ошибка API сервера: %w", startErr)
		}
	}()

	appLogger.Info("")
	appLogger.Info("Прокси-клиент готов к работе")
	appLogger.Info("UI:     http://localhost%s", apiListenAddress)
	appLogger.Info("API:    http://localhost%s/api/status", apiListenAddress)
	appLogger.Info("Rules:  http://localhost%s/api/tun/rules", apiListenAddress)
	appLogger.Info("")

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	select {
	case <-quit:
		appLogger.Info("Получен сигнал остановки")
	case apiErr := <-apiErrChan:
		appLogger.Error("API сервер упал: %v", apiErr)
	}

	// Graceful shutdown
	appLogger.Info("Завершение работы...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

	if err := apiServer.Shutdown(shutdownCtx); err != nil {
		appLogger.Error("Ошибка при остановке API сервера: %v", err)
	}

	if processMonitor != nil {
		if err := processMonitor.Stop(); err != nil {
			appLogger.Error("Ошибка при остановке process monitor: %v", err)
		}
	}

	appLogger.Info("Остановка XRay...")
	if err := xrayManager.Stop(); err != nil {
		appLogger.Error("Ошибка при остановке XRay: %v", err)
	}

	appLogger.Info("Отключение системного прокси...")
	if err := proxyManager.Disable(); err != nil {
		appLogger.Error("Ошибка при отключении прокси: %v", err)
	}

	appLogger.Info("Работа завершена корректно")
	return nil
}
