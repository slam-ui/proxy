package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"time"

	"proxyclient/internal/api"
	"proxyclient/internal/apprules"
	"proxyclient/internal/config"
	"proxyclient/internal/eventlog"
	"proxyclient/internal/logger"
	"proxyclient/internal/netutil"
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
	logFile          = "proxy-client.log"
	apiListenAddress = ":8080"
	webUIURL         = "http://localhost:8080"
	shutdownTimeout  = 10 * time.Second
)

// openLogFile открывает файл лога в режиме append|create рядом с exe.
// Логи сохраняются даже если UI не запустился — для анализа краша.
func openLogFile() (*os.File, error) {
	exe, err := os.Executable()
	if err != nil {
		exe = "."
	}
	logPath := filepath.Join(filepath.Dir(exe), logFile)
	return os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
}

func main() {
	// ── Открываем файл лога ДО всего остального ──────────────────────────────
	// Если приложение падает раньше чем поднимается UI или API,
	// proxy-client.log содержит полную причину.
	lf, fileErr := openLogFile()
	if fileErr != nil {
		fmt.Fprintf(os.Stderr, "[WARN] Не удалось открыть файл лога %s: %v\n", logFile, fileErr)
	}
	if lf != nil {
		defer func() { _ = lf.Sync(); _ = lf.Close() }()
	}

	// Весь вывод — и в консоль, и в файл
	var output io.Writer = os.Stdout
	if lf != nil {
		output = io.MultiWriter(os.Stdout, lf)
	}

	// ── Перехватываем panic — пишем стектрейс в лог до выхода ───────────────
	defer func() {
		if r := recover(); r != nil {
			ts := time.Now().Format("2006-01-02 15:04:05")
			fmt.Fprintf(output, "\n[%s] ══ PANIC ══════════════════════════\n", ts)
			fmt.Fprintf(output, "[%s] %v\n", ts, r)
			fmt.Fprintf(output, "%s\n", debug.Stack())
			fmt.Fprintf(output, "[%s] ══════════════════════════════════\n", ts)
			if lf != nil {
				_ = lf.Sync()
			}
			os.Exit(2)
		}
	}()

	// Разделитель сессий в файле
	fmt.Fprintf(output, "\n══════════════════════════════════════════════════\n")
	fmt.Fprintf(output, " Старт: %s\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(output, "══════════════════════════════════════════════════\n")

	if err := run(output); err != nil {
		fmt.Fprintf(output, "[FATAL] %v\n", err)
		if lf != nil {
			_ = lf.Sync()
		}
		log.Fatalf("Приложение завершилось с ошибкой: %v", err)
	}
}

func run(output io.Writer) error {
	// Базовый логгер пишет в output (stdout + файл)
	appLogger := logger.New(logger.Config{
		Level:  logger.InfoLevel,
		Output: output,
	})

	// EventLog — кольцевой буфер на 500 событий, доступен через /api/events
	evLog := eventlog.New(500)
	mainLogger := eventlog.NewLogger(appLogger, evLog, "main")

	mainLogger.Info("Запуск прокси-клиента...")

	// Адаптеры для дочерних компонентов
	proxyLogger   := eventlog.NewLogger(appLogger, evLog, "proxy")
	xrayLogger    := eventlog.NewLogger(appLogger, evLog, "xray")
	monitorLogger := eventlog.NewLogger(appLogger, evLog, "monitor")

	// 1. Прокси-менеджер
	proxyManager := proxy.NewManager(proxyLogger)
	proxyConfig := proxy.Config{
		Address:  api.DefaultProxyAddress,
		Override: "<local>",
	}

	// 2. Очистка старого TUN перед запуском
	exec.Command("netsh", "interface", "delete", "interface", config.TunInterfaceName).Run() //nolint:errcheck

	// 3. Запускаем sing-box
	mainLogger.Info("Запуск sing-box...")
	routingCfg, err := config.LoadRoutingConfig("routing.json")
	if err != nil {
		mainLogger.Warn("Не удалось загрузить routing config: %v, используем дефолтный", err)
		routingCfg = config.DefaultRoutingConfig()
	}
	if err := config.GenerateSingBoxConfig(secretFile, "config.singbox.json", routingCfg); err != nil {
		mainLogger.Warn("Не удалось сгенерировать sing-box конфиг: %v", err)
		mainLogger.Warn("Sing-box будет запущен с СУЩЕСТВУЮЩИМ конфигом")
	}

	xrayCfg := xray.Config{
		ExecutablePath: "./sing-box.exe",
		ConfigPath:     "config.singbox.json",
		SecretKeyPath:  secretFile,
		Args:           []string{"run"},
		Logger:         xrayLogger,
		SingBoxWriter:  eventlog.NewLineWriter(evLog, "sing-box", eventlog.LevelInfo),
	}
	xrayManager, err := xray.NewManager(xrayCfg)
	if err != nil {
		return fmt.Errorf("не удалось запустить sing-box: %w", err)
	}
	mainLogger.Info("Sing-box запущен (PID: %d)", xrayManager.GetPID())

	// 4. Ждём готовности sing-box перед включением системного прокси
	mainLogger.Info("Ожидание готовности sing-box на %s...", api.DefaultProxyAddress)
	if err := netutil.WaitForPort(api.DefaultProxyAddress, 15*time.Second); err != nil {
		mainLogger.Warn("sing-box не ответил за 15с: %v — включаем прокси всё равно", err)
	} else {
		mainLogger.Info("sing-box готов")
	}
	if err := proxyManager.Enable(proxyConfig); err != nil {
		mainLogger.Warn("Не удалось включить системный прокси: %v", err)
	} else {
		mainLogger.Info("Системный прокси включён: %s", proxyConfig.Address)
	}

	// 5. Per-app proxy rules
	storage := apprules.NewFileStorage(appRulesFile)
	rulesEngine, err := apprules.NewPersistentEngine(storage)
	if err != nil {
		mainLogger.Warn("Failed to initialize rules engine: %v", err)
		rulesEngine = nil
	} else {
		mainLogger.Info("Rules engine инициализирован (%d правил)", len(rulesEngine.ListRules()))
	}

	var processMonitor process.Monitor
	var processLauncher process.Launcher
	if rulesEngine != nil {
		processMonitor = process.NewMonitor(monitorLogger)
		if err := processMonitor.Start(); err != nil {
			mainLogger.Warn("Failed to start process monitor: %v", err)
		} else {
			mainLogger.Info("Process monitor запущен")
		}
		processLauncher = process.NewLauncher(monitorLogger, rulesEngine)
	}

	// 6. API сервер
	apiServer := api.NewServer(api.Config{
		ListenAddress: apiListenAddress,
		XRayManager:   xrayManager,
		ProxyManager:  proxyManager,
		ConfigPath:    runtimeFile,
		Logger:        mainLogger,
		EventLog:      evLog,
	})
	apiServer.SetupTunRoutes(xrayCfg)
	if rulesEngine != nil && processMonitor != nil && processLauncher != nil {
		apiServer.SetupAppProxyRoutes(rulesEngine, processMonitor, processLauncher)
	}
	apiServer.FinalizeRoutes()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Канал — закрывается когда HTTP-сервер реально готов принимать соединения.
	// BUG FIX: раньше window.Open вызывался сразу после старта горутины сервера.
	// WebView2 пытался открыть localhost:8080 до того как сервер занял порт,
	// получал "connection refused" и зависал в error-recovery loop, блокируя
	// Win32 message pump → окно показывало "Не отвечает".
	apiReady := make(chan struct{})

	go func() {
		if startErr := apiServer.Start(ctx); startErr != nil {
			mainLogger.Error("Ошибка API сервера: %v", startErr)
		}
	}()

	go func() {
		// Ждём пока сервер реально начнёт принимать соединения
		if waitErr := netutil.WaitForPort("localhost:8080", 5*time.Second); waitErr == nil {
			mainLogger.Info("API сервер готов на :8080")
		} else {
			mainLogger.Warn("API сервер не ответил за 5с: %v", waitErr)
		}
		close(apiReady)
	}()

	mainLogger.Info("Инициализация завершена")
	quit := make(chan struct{})

	// 7. Открываем окно только когда ОБА условия выполнены:
	//    а) трей полностью инициализирован (WaitReady)
	//    б) API сервер реально слушает :8080 (apiReady)
	go func() {
		tray.WaitReady()
		tray.SetEnabled(true)
		mainLogger.Info("Трей готов, ожидаем API сервер...")
		<-apiReady
		mainLogger.Info("Открываем панель управления: %s", webUIURL)
		window.Open(webUIURL)
	}()

	// 8. Трей блокирует main goroutine (требование Windows COM STA)
	tray.Run(tray.Callbacks{
		OnOpen: func() {
			window.Open(webUIURL)
		},
		OnEnable: func() {
			if err := proxyManager.Enable(proxyConfig); err != nil {
				mainLogger.Error("Ошибка включения прокси: %v", err)
			} else {
				tray.SetEnabled(true)
				mainLogger.Info("Прокси включён через трей")
			}
		},
		OnDisable: func() {
			if err := proxyManager.Disable(); err != nil {
				mainLogger.Error("Ошибка отключения прокси: %v", err)
			} else {
				tray.SetEnabled(false)
				mainLogger.Info("Прокси отключён через трей")
			}
		},
		OnQuit: func() {
			close(quit)
		},
	})

	<-quit

	// 9. Graceful shutdown
	mainLogger.Info("Завершение работы...")
	window.Close()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

	if err := apiServer.Shutdown(shutdownCtx); err != nil {
		mainLogger.Error("Ошибка при остановке API сервера: %v", err)
	}
	if processMonitor != nil {
		processMonitor.Stop()
	}
	if err := xrayManager.Stop(); err != nil {
		mainLogger.Error("Ошибка при остановке sing-box: %v", err)
	}
	if err := proxyManager.Disable(); err != nil {
		mainLogger.Error("Ошибка при отключении прокси: %v", err)
	}

	mainLogger.Info("Работа завершена корректно")
	return nil
}
