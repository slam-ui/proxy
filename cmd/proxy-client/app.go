package main

// App инкапсулирует весь lifecycle приложения.
//
// Выделение из main.go решает две проблемы:
//  1. Тестируемость — можно создать App с моками и тестировать поведение
//     (включение прокси, обработка крашей) без запуска реального sing-box.
//  2. Читаемость — run() становится ~30 строк; каждый аспект (wintun, API,
//     фоновая инициализация) вынесен в отдельный метод.

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"proxyclient/internal/anomalylog"
	"proxyclient/internal/api"
	"proxyclient/internal/config"
	"proxyclient/internal/eventlog"
	"proxyclient/internal/logger"
	"proxyclient/internal/netutil"
	"proxyclient/internal/notification"
	"proxyclient/internal/process"
	"proxyclient/internal/proxy"
	"proxyclient/internal/tray"
	"proxyclient/internal/window"
	"proxyclient/internal/wintun"
	"proxyclient/internal/xray"
)

// AppConfig содержит все настраиваемые параметры приложения.
type AppConfig struct {
	SecretFile   string
	SingBoxPath  string
	ConfigPath   string
	RuntimeFile  string
	DataDir      string
	AppRulesFile string
	APIAddress   string
	WebUIURL     string
}

// DefaultAppConfig возвращает production конфигурацию.
func DefaultAppConfig() AppConfig {
	return AppConfig{
		SecretFile:   "secret.key",
		SingBoxPath:  "./sing-box.exe",
		ConfigPath:   "config.singbox.json",
		RuntimeFile:  "config.runtime.json",
		DataDir:      config.DataDir,
		AppRulesFile: config.DataDir + "/app_rules.json",
		APIAddress:   config.APIAddress,
		WebUIURL:     "http://localhost:8080",
	}
}

// App управляет полным жизненным циклом прокси-клиента.
type App struct {
	cfg          AppConfig
	output       io.Writer
	appLogger    logger.Logger
	evLog        *eventlog.Log
	mainLogger   logger.Logger
	proxyManager proxy.Manager
	apiServer    *api.Server
	quit         chan struct{}
	anomaly      *anomalylog.Detector
}

// NewApp создаёт App и базовые инфраструктурные компоненты (логгер, eventlog, anomaly detector).
func NewApp(cfg AppConfig, output io.Writer) *App {
	appLogger := logger.New(logger.Config{
		Level:  logger.InfoLevel,
		Output: output,
	})
	evLog := eventlog.New(500)
	mainLogger := eventlog.NewLogger(appLogger, evLog, "main")

	exeDir := "."
	if exe, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(exe)
	}
	anomalyDetector := anomalylog.New(evLog, exeDir)

	proxyLogger := eventlog.NewLogger(appLogger, evLog, "proxy")
	proxyManager := proxy.NewManager(proxyLogger)

	return &App{
		cfg:          cfg,
		output:       output,
		appLogger:    appLogger,
		evLog:        evLog,
		mainLogger:   mainLogger,
		proxyManager: proxyManager,
		quit:         make(chan struct{}),
		anomaly:      anomalyDetector,
	}
}

// buildXRayCfg собирает xray.Config с BeforeRestart и OnCrash.
func (a *App) buildXRayCfg() xray.Config {
	xrayLogger := eventlog.NewLogger(a.appLogger, a.evLog, "xray")
	proxyConfig := proxy.Config{
		Address:  config.ProxyAddr,
		Override: "<local>",
	}
	return xray.Config{
		ExecutablePath: a.cfg.SingBoxPath,
		ConfigPath:     a.cfg.ConfigPath,
		SecretKeyPath:  a.cfg.SecretFile,
		Args:           []string{"run", "--disable-color"},
		Logger:         xrayLogger,
		SingBoxWriter:  eventlog.NewLineWriter(a.evLog, "sing-box", eventlog.LevelInfo),
		FileWriter:     a.output,
		// BeforeRestart — wintun cleanup. api пакет больше не зависит от wintun.
		BeforeRestart: func(log logger.Logger) error {
			wintun.RecordStop()
			wintun.RemoveStaleTunAdapter(log)
			wintun.PollUntilFree(log, config.TunInterfaceName)
			return nil
		},
		OnCrash: func(crashErr error) {
			a.handleCrash(crashErr, proxyConfig)
		},
	}
}

// handleCrash обрабатывает неожиданное падение sing-box.
// Вынесен из замыкания — теперь тестируем и читаем как самостоятельный метод.
func (a *App) handleCrash(crashErr error, proxyConfig proxy.Config) {
	if xray.IsTooManyRestarts(crashErr) {
		a.mainLogger.Error("[Error] Core exits too frequently — авторестарт отключён")
		notification.Send("Proxy — ошибка", "Частые сбои sing-box. Откройте приложение.")
		_ = a.proxyManager.Disable()
		tray.SetEnabled(false)
		return
	}

	currentMgr := a.apiServer.GetXRayManager()
	if currentMgr == nil {
		return
	}

	lastOut := currentMgr.LastOutput()
	isTunConflict := strings.Contains(lastOut, "Cannot create a file when that file already exists") ||
		strings.Contains(lastOut, "configure tun interface")

	if isTunConflict {
		wintun.RecordStop()
		wintun.IncreaseAdaptiveGap(a.mainLogger)
		a.mainLogger.Warn("Detected wintun conflict — ждём освобождения wintun kernel-объекта...")
		notification.Send("Proxy", "Перезапуск TUN...")
		wintun.RemoveStaleTunAdapter(a.mainLogger)
		wintun.PollUntilFree(a.mainLogger, config.TunInterfaceName)
		a.mainLogger.Info("Перезапуск sing-box после wintun GC...")
		if startErr := currentMgr.Start(); startErr != nil {
			a.mainLogger.Error("Не удалось перезапустить sing-box: %v", startErr)
			notification.Send("Proxy — ошибка", "Не удалось перезапустить. Откройте приложение.")
			_ = a.proxyManager.Disable()
			tray.SetEnabled(false)
		} else {
			a.mainLogger.Info("sing-box успешно перезапущен (PID: %d)", currentMgr.GetPID())
			notification.Send("Proxy", "Перезапущен успешно ✓")
		}
		return
	}

	a.mainLogger.Error("sing-box упал неожиданно (%v) — отключаем системный прокси", crashErr)
	notification.Send("Proxy — ошибка", "sing-box упал. Проверьте лог и перезапустите.")
	if disableErr := a.proxyManager.Disable(); disableErr != nil {
		a.mainLogger.Error("Не удалось отключить прокси: %v", disableErr)
	} else {
		tray.SetEnabled(false)
		a.mainLogger.Warn("Системный прокси отключён из-за краша sing-box.")
	}
}

// startBackground запускает фоновую горутину: wintun cleanup → sing-box → proxy enable.
// UI уже работает пока эта горутина выполняется — пользователь видит ПРОГРЕВ...
func (a *App) startBackground(xrayCfg xray.Config, proxyConfig proxy.Config, apiReady <-chan struct{}) {
	go func() {
		<-apiReady

		a.mainLogger.Info("Фоновая инициализация: wintun cleanup...")
		tray.SetEnabled(false)

		wintun.RemoveStaleTunAdapter(a.mainLogger)
		wintun.PollUntilFree(a.mainLogger, config.TunInterfaceName)

		if _, err := os.Stat(a.cfg.ConfigPath + ".pending"); err == nil {
			_ = os.Remove(a.cfg.ConfigPath + ".pending")
			a.mainLogger.Info("Удалён осиротевший .pending конфиг от предыдущего запуска")
		}

		routingCfg, err := config.LoadRoutingConfig(a.cfg.DataDir + "/routing.json")
		if err != nil {
			a.mainLogger.Warn("Не удалось загрузить routing config: %v, используем дефолтный", err)
			routingCfg = config.DefaultRoutingConfig()
		}

		a.mainLogger.Info("Запуск sing-box...")
		if genErr := config.GenerateSingBoxConfig(a.cfg.SecretFile, a.cfg.ConfigPath, routingCfg); genErr != nil {
			if !a.handleConfigError(genErr) {
				return
			}
		}

		xrayManager, err := xray.NewManager(xrayCfg)
		if err != nil {
			a.mainLogger.Error("Не удалось запустить sing-box: %v", err)
			notification.Send("Proxy — ошибка", "Не удалось запустить sing-box. Проверьте лог.")
			return
		}
		a.mainLogger.Info("Sing-box запущен (PID: %d)", xrayManager.GetPID())
		a.apiServer.SetXRayManager(xrayManager)

		a.mainLogger.Info("Ожидание готовности sing-box на %s...", config.ProxyAddr)
		if err := netutil.WaitForPort(config.ProxyAddr, 15*time.Second); err != nil {
			a.mainLogger.Warn("sing-box не ответил за 15с: %v — включаем прокси всё равно", err)
		} else {
			a.mainLogger.Info("sing-box готов")
		}

		if err := a.proxyManager.Enable(proxyConfig); err != nil {
			a.mainLogger.Warn("Не удалось включить системный прокси: %v", err)
		} else {
			a.mainLogger.Info("Системный прокси включён: %s", proxyConfig.Address)
		}

		tray.SetEnabled(true)
		notification.Send("Proxy", "Прокси готов ✓")
		a.mainLogger.Info("Фоновая инициализация завершена")
	}()
}

// handleConfigError обрабатывает ошибку генерации конфига.
// Возвращает true если можно продолжить (запустить с существующим конфигом).
func (a *App) handleConfigError(err error) bool {
	secretData, readErr := os.ReadFile(a.cfg.SecretFile)
	if readErr != nil || len(secretData) == 0 {
		a.mainLogger.Error("файл %s не найден или пуст — вставьте вашу VLESS-ссылку", a.cfg.SecretFile)
		notification.Send("Proxy — ошибка", "Вставьте VLESS-ссылку в secret.key")
		return false
	}
	allComments := true
	for _, line := range strings.Split(strings.TrimSpace(string(secretData)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			allComments = false
			break
		}
	}
	if allComments {
		a.mainLogger.Error("файл %s содержит только комментарии — вставьте вашу VLESS-ссылку", a.cfg.SecretFile)
		notification.Send("Proxy — ошибка", "Вставьте VLESS-ссылку в secret.key")
		return false
	}
	a.mainLogger.Warn("Не удалось сгенерировать sing-box конфиг: %v", err)
	a.mainLogger.Warn("Sing-box будет запущен с СУЩЕСТВУЮЩИМ конфигом")
	return true
}

// Shutdown корректно останавливает все компоненты.
func (a *App) Shutdown(shutdownCtx context.Context, processMonitor process.Monitor) {
	window.Close()

	if a.apiServer != nil {
		if err := a.apiServer.Shutdown(shutdownCtx); err != nil {
			a.mainLogger.Error("Ошибка при остановке API сервера: %v", err)
		}
	}
	if processMonitor != nil {
		processMonitor.Stop()
	}
	if xrayMgr := a.apiServer.GetXRayManager(); xrayMgr != nil {
		if err := xrayMgr.Stop(); err != nil {
			a.mainLogger.Error("Ошибка при остановке sing-box: %v", err)
		}
	}

	wintun.RecordStop()
	wintun.ResetAdaptiveGap()
	a.mainLogger.Info("Очистка TUN адаптера при выходе...")
	wintun.Shutdown(a.mainLogger)

	if err := a.proxyManager.Disable(); err != nil {
		a.mainLogger.Error("Ошибка при отключении прокси: %v", err)
	}
	a.mainLogger.Info("Работа завершена корректно")
}
