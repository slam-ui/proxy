package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
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
	appLogger := logger.New(logger.Config{
		Level:  logger.InfoLevel,
		Output: os.Stdout,
	})

	appLogger.Info("Запуск прокси-клиента...")

	// 1. Генерируем конфигурацию
	appLogger.Info("Генерация конфигурации...")

	appLogger.Info("Конфигурация успешно сгенерирована")

	// 2. Прокси-менеджер
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
	// Очистка старого TUN перед запуском
	exec.Command("netsh", "interface", "delete", "interface", "tun0").Run()

	// 4. Запускаем XRay
	appLogger.Info("Запуск XRay...")
	// Генерируем начальный sing-box конфиг
	routingCfg, _ := config.LoadRoutingConfig("routing.json")
	if err := config.GenerateSingBoxConfig(secretFile, "config.singbox.json", routingCfg); err != nil {
		appLogger.Warn("Не удалось сгенерировать sing-box конфиг: %v", err)
	}

	xrayCfg := xray.Config{
		ExecutablePath: "./sing-box.exe",
		ConfigPath:     "config.singbox.json",
		Args:           []string{"run"}, // ← добавить это
		Logger:         appLogger,
	}
	xrayManager, err := xray.NewManager(xrayCfg)
	if err != nil {
		_ = proxyManager.Disable()
		return fmt.Errorf("не удалось запустить XRay: %w", err)
	}
	appLogger.Info("XRay запущен (PID: %d)", xrayManager.GetPID())

	// 5. Per-app proxy
	appLogger.Info("Инициализация per-app proxy...")
	storage := apprules.NewFileStorage(appRulesFile)
	rulesEngine, err := apprules.NewPersistentEngine(storage)
	if err != nil {
		appLogger.Warn("Failed to initialize rules engine: %v", err)
		rulesEngine = nil
	} else {
		appLogger.Info("Rules engine инициализирован (%d правил)", len(rulesEngine.ListRules()))
	}

	var processMonitor process.Monitor
	var processLauncher process.Launcher

	if rulesEngine != nil {
		processMonitor = process.NewMonitor(appLogger)
		if err := processMonitor.Start(); err != nil {
			appLogger.Warn("Failed to start process monitor: %v", err)
		} else {
			appLogger.Info("Process monitor запущен")
		}
		processLauncher = process.NewLauncher(appLogger, rulesEngine)
		appLogger.Info("Per-app proxy инициализирован")
	}

	// 6. API сервер
	apiServer := api.NewServer(api.Config{
		ListenAddress: apiListenAddress,
		XRayManager:   xrayManager,
		ProxyManager:  proxyManager,
		ConfigPath:    runtimeFile,
		Logger:        appLogger,
	})

	// Регистрируем маршруты — порядок важен, статика всегда последняя
	apiServer.SetupTunRoutes(xrayCfg)
	appLogger.Info("TUN routes зарегистрированы")

	if rulesEngine != nil && processMonitor != nil && processLauncher != nil {
		apiServer.SetupAppProxyRoutes(rulesEngine, processMonitor, processLauncher)
		appLogger.Info("Per-app proxy routes зарегистрированы")
	}

	apiServer.FinalizeRoutes() // статика — последней

	// 7. Запускаем сервер
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	apiErrChan := make(chan error, 1)
	go func() {
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

	// 8. Ждём сигнал
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	select {
	case <-quit:
		appLogger.Info("Получен сигнал остановки")
	case apiErr := <-apiErrChan:
		appLogger.Error("API сервер упал: %v", apiErr)
	}

	// 9. Graceful shutdown
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
