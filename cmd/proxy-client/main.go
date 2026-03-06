package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"time"

	"proxyclient/internal/api"
	"proxyclient/internal/apprules"
	"proxyclient/internal/config"
	"proxyclient/internal/logger"
	"proxyclient/internal/process"
	"proxyclient/internal/proxy"
	"proxyclient/internal/tray"
	"proxyclient/internal/window"
	"proxyclient/internal/xray"
)

const (
	secretFile       = "secret.key"
	runtimeFile      = "config.runtime.json"
	appRulesFile     = "app_rules.json"
	apiListenAddress = ":8080"
	webUIURL         = "http://localhost:8080"
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

	// 1. Прокси-менеджер
	proxyManager := proxy.NewManager(appLogger)

	// 2. Включаем системный прокси
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

	// 3. Запускаем sing-box
	appLogger.Info("Запуск XRay...")
	routingCfg, _ := config.LoadRoutingConfig("routing.json")
	if err := config.GenerateSingBoxConfig(secretFile, "config.singbox.json", routingCfg); err != nil {
		appLogger.Warn("Не удалось сгенерировать sing-box конфиг: %v", err)
	}

	xrayCfg := xray.Config{
		ExecutablePath: "./sing-box.exe",
		ConfigPath:     "config.singbox.json",
		Args:           []string{"run"},
		Logger:         appLogger,
	}
	xrayManager, err := xray.NewManager(xrayCfg)
	if err != nil {
		_ = proxyManager.Disable()
		return fmt.Errorf("не удалось запустить XRay: %w", err)
	}
	appLogger.Info("XRay запущен (PID: %d)", xrayManager.GetPID())

	// 4. Per-app proxy
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
	}

	// 5. API сервер
	apiServer := api.NewServer(api.Config{
		ListenAddress: apiListenAddress,
		XRayManager:   xrayManager,
		ProxyManager:  proxyManager,
		ConfigPath:    runtimeFile,
		Logger:        appLogger,
	})

	apiServer.SetupTunRoutes(xrayCfg)
	if rulesEngine != nil && processMonitor != nil && processLauncher != nil {
		apiServer.SetupAppProxyRoutes(rulesEngine, processMonitor, processLauncher)
	}
	apiServer.FinalizeRoutes()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if startErr := apiServer.Start(ctx); startErr != nil {
			appLogger.Error("Ошибка API сервера: %v", startErr)
		}
	}()

	appLogger.Info("Прокси-клиент готов к работе — иконка в трее")

	// 6. Канал завершения
	quit := make(chan struct{})

	// 7. Трей — блокирует текущий поток (требование Windows)
	// SetEnabled вызываем внутри onReady через отдельную горутину
	go func() {
		// Небольшая задержка чтобы трей успел инициализироваться
		time.Sleep(200 * time.Millisecond)
		tray.SetEnabled(true)
		// Автоматически открываем окно при старте
		window.Open(webUIURL)
	}()

	tray.Run(tray.Callbacks{
		OnOpen: func() {
			window.Open(webUIURL)
		},
		OnEnable: func() {
			if err := proxyManager.Enable(proxyConfig); err != nil {
				appLogger.Error("Ошибка включения прокси: %v", err)
			} else {
				tray.SetEnabled(true)
				appLogger.Info("Прокси включён через трей")
			}
		},
		OnDisable: func() {
			if err := proxyManager.Disable(); err != nil {
				appLogger.Error("Ошибка отключения прокси: %v", err)
			} else {
				tray.SetEnabled(false)
				appLogger.Info("Прокси отключён через трей")
			}
		},
		OnQuit: func() {
			close(quit)
		},
	})

	// Ждём сигнал завершения от трея
	<-quit

	// 8. Graceful shutdown
	appLogger.Info("Завершение работы...")
	window.Close()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

	if err := apiServer.Shutdown(shutdownCtx); err != nil {
		appLogger.Error("Ошибка при остановке API сервера: %v", err)
	}
	if processMonitor != nil {
		processMonitor.Stop()
	}
	if err := xrayManager.Stop(); err != nil {
		appLogger.Error("Ошибка при остановке XRay: %v", err)
	}
	if err := proxyManager.Disable(); err != nil {
		appLogger.Error("Ошибка при отключении прокси: %v", err)
	}

	appLogger.Info("Работа завершена корректно")
	return nil
}
