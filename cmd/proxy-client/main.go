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
	// Инициализируем логгер
	appLogger := logger.New(logger.Config{
		Level:  logger.InfoLevel,
		Output: os.Stdout,
	})

	appLogger.Info("Запуск прокси-клиента...")

	// 1. Генерируем конфигурацию
	appLogger.Info("Генерация конфигурации...")
	if err := config.GenerateRuntimeConfig(templateFile, secretFile, runtimeFile); err != nil {
		return fmt.Errorf("не удалось сгенерировать конфигурацию: %w", err)
	}
	appLogger.Info("Конфигурация успешно сгенерирована")

	// 2. Создаём прокси-менеджер
	proxyManager := proxy.NewManager(appLogger)

	// 3. Включаем системный прокси
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

	// 4. Запускаем XRay
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

	// 5. Initialize per-app proxy components
	appLogger.Info("Инициализация per-app proxy...")

	// Create rules storage
	storage := apprules.NewFileStorage(appRulesFile)

	// Create persistent rules engine
	rulesEngine, err := apprules.NewPersistentEngine(storage)
	if err != nil {
		appLogger.Warn("Failed to initialize rules engine: %v", err)
		rulesEngine = nil // Continue without per-app proxy
	} else {
		rules := rulesEngine.ListRules()
		appLogger.Info("Rules engine initialized with %d rules", len(rules))
	}

	var processMonitor process.Monitor
	var processLauncher process.Launcher

	if rulesEngine != nil {
		// Create process monitor
		processMonitor = process.NewMonitor(appLogger)
		if err := processMonitor.Start(); err != nil {
			appLogger.Warn("Failed to start process monitor: %v", err)
		} else {
			appLogger.Info("Process monitor started")
		}

		// Create process launcher
		processLauncher = process.NewLauncher(appLogger, rulesEngine)

		appLogger.Info("Per-app proxy initialized successfully")
	}

	// 6. Запускаем HTTP API
	apiServer := api.NewServer(api.Config{
		ListenAddress: apiListenAddress,
		XRayManager:   xrayManager,
		ProxyManager:  proxyManager,
		ConfigPath:    runtimeFile,
		Logger:        appLogger,
	})

	// Setup per-app proxy routes if available
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
	appLogger.Info("API: http://localhost%s", apiListenAddress)
	appLogger.Info("Status: http://localhost%s/api/status", apiListenAddress)
	appLogger.Info("Per-App: http://localhost%s/api/apps/rules", apiListenAddress)
	appLogger.Info("")

	// 7. Ожидание сигнала завершения
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	select {
	case <-quit:
		appLogger.Info("Получен сигнал остановки")
	case apiErr := <-apiErrChan:
		appLogger.Error("API сервер упал: %v", apiErr)
	}

	// 8. Graceful shutdown
	appLogger.Info("Начало корректного завершения работы...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

	// Останавливаем API
	if err := apiServer.Shutdown(shutdownCtx); err != nil {
		appLogger.Error("Ошибка при остановке API сервера: %v", err)
	}

	// Останавливаем process monitor
	if processMonitor != nil {
		if err := processMonitor.Stop(); err != nil {
			appLogger.Error("Ошибка при остановке process monitor: %v", err)
		}
	}

	// Останавливаем XRay
	appLogger.Info("Остановка XRay...")
	if err := xrayManager.Stop(); err != nil {
		appLogger.Error("Ошибка при остановке XRay: %v", err)
	}

	// Отключаем системный прокси
	appLogger.Info("Отключение системного прокси...")
	if err := proxyManager.Disable(); err != nil {
		appLogger.Error("Ошибка при отключении прокси: %v", err)
	}

	appLogger.Info("Работа завершена корректно")

	return nil
}
