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
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"proxyclient/internal/anomalylog"
	"proxyclient/internal/api"
	"proxyclient/internal/config"
	"proxyclient/internal/engine"
	"proxyclient/internal/eventlog"
	"proxyclient/internal/killswitch"
	"proxyclient/internal/logger"
	"proxyclient/internal/netutil"
	"proxyclient/internal/notification"
	"proxyclient/internal/process"
	"proxyclient/internal/proxy"
	"proxyclient/internal/tray"
	"proxyclient/internal/turnmanager"
	"proxyclient/internal/turnproxy"
	"proxyclient/internal/window"
	"proxyclient/internal/wintun"
	"proxyclient/internal/xray"
)

// AppConfig содержит все настраиваемые параметры приложения.
type AppConfig struct {
	KillSwitch        bool // блокировать трафик при падении туннеля
	ProxyGuardEnabled bool // B-2: включить Proxy Guard для восстановления системного прокси
	// B-10: автообновление geosite баз данных
	GeoAutoUpdateEnabled      bool
	GeoAutoUpdateIntervalDays int
	WarmUpHosts               []string
	SecretFile                string
	SingBoxPath               string
	ConfigPath                string
	RuntimeFile               string
	DataDir                   string
	AppRulesFile              string
	APIAddress                string
	WebUIURL                  string
	// TURNRelayPort — UDP порт серверного vk-turn-relay (default: 3478).
	// Переопределяется флагом -turn-port при запуске.
	// Порт 443 UDP — альтернатива если 3478 заблокирован провайдером.
	TURNRelayPort      int
	ProxyGuardInterval time.Duration // B-2: интервал проверки Proxy Guard (default 5s)
}

// DefaultAppConfig возвращает production конфигурацию.
func DefaultAppConfig() AppConfig {
	return AppConfig{
		KillSwitch:                false,
		ProxyGuardEnabled:         true, // B-2: default включить
		GeoAutoUpdateEnabled:      true, // B-10: автообновление geosite по умолчанию
		GeoAutoUpdateIntervalDays: 7,    // B-10: обновлять раз в 7 дней
		WarmUpHosts: []string{
			"https://api.ipify.org?format=json",
			"https://api64.ipify.org?format=json",
			"https://ifconfig.me/ip",
		},
		SecretFile:         "secret.key",
		SingBoxPath:        "./sing-box.exe",
		ConfigPath:         "config.singbox.json",
		RuntimeFile:        "config.runtime.json",
		DataDir:            config.DataDir,
		AppRulesFile:       config.DataDir + "/app_rules.json",
		APIAddress:         config.APIAddress,
		WebUIURL:           "http://localhost:8080",
		TURNRelayPort:      3478,
		ProxyGuardInterval: 5 * time.Second, // B-2: проверка каждые 5 секунд
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
	// lifecycleCtx отменяется при Shutdown — прерывает PollUntilFree во всех горутинах.
	lifecycleCtx     context.Context
	lifecycleCancel  context.CancelFunc
	cachedServerIPMu sync.Mutex
	cachedServerIP   string
	cachedForFile    string
	cachedSecretHash [32]byte
	// turnProxy — локальный TURN прокси (nil когда неактивен).
	// Запускается в applyTURNMode(ModeTURN), останавливается в applyTURNMode(ModeDirect).
	turnProxy *turnproxy.Proxy
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

	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())

	return &App{
		cfg:             cfg,
		output:          output,
		appLogger:       appLogger,
		evLog:           evLog,
		mainLogger:      mainLogger,
		proxyManager:    proxyManager,
		quit:            make(chan struct{}),
		anomaly:         anomalyDetector,
		lifecycleCtx:    lifecycleCtx,
		lifecycleCancel: lifecycleCancel,
	}
}

// buildXRayCfg собирает xray.Config с BeforeRestart и OnCrash.
func (a *App) buildXRayCfg() xray.Config {
	xrayLogger := eventlog.NewLogger(a.appLogger, a.evLog, "xray")
	return xray.Config{
		ExecutablePath: a.cfg.SingBoxPath,
		ConfigPath:     a.cfg.ConfigPath,
		SecretKeyPath:  a.cfg.SecretFile,
		Args:           []string{"run", "--disable-color"},
		Logger:         xrayLogger,
		SingBoxWriter:  eventlog.NewLineWriter(a.evLog, "sing-box", eventlog.LevelInfo),
		FileWriter:     xray.NewFilterWriter(a.output),
		// BeforeRestart — wintun cleanup для обычного перезапуска (apply rules, ручной restart).
		//
		// BUG FIX: ранее использовался quickCtx с таймаутом 30s, но wintun GC gap = 60s.
		// Через 30s quickCtx истекал → PollUntilFree возвращался досрочно → sing-box стартовал
		// пока wintun kernel-объект ещё не освободился → FATAL "configure tun interface" краш.
		// handleCrash затем увеличивал gap до 120s → пользователь ждал 2+ минуты без прокси.
		//
		// Исправление: используем ctx (lifecycleCtx) без дополнительного таймаута — ждём
		// ровно столько, сколько нужно wintun. При чистом завершении (RecordCleanShutdown)
		// PollUntilFree возвращается мгновенно, задержка возникает только при крашах.
		BeforeRestart: func(ctx context.Context, log logger.Logger) error {
			// СР-3: проверяем CleanShutdownFile ДО RecordStop, который его удаляет.
			// Если sing-box остановился чисто — пропускаем RemoveStaleTunAdapter (~3-5с).
			_, cleanErr := os.Stat(wintun.CleanShutdownFile)
			wintun.RecordStop()
			if cleanErr != nil {
				wintun.RemoveStaleTunAdapter(log)
			} else {
				log.Info("wintun: чистое завершение — пропускаем RemoveStaleTunAdapter в BeforeRestart")
			}
			wintun.PollUntilFree(ctx, log, config.TunInterfaceName)
			return nil
		},
		// ОПТИМИЗАЦИЯ: при чистом graceful stop записываем маркер.
		// Следующий PollUntilFree (при apply rules или рестарте) пропустит
		// все задержки (gap=0, settle=0) — sing-box стартует мгновенно.
		OnGracefulStop: func() {
			wintun.RecordCleanShutdown()
		},
		OnCrash: func(crashErr error, crashedManager xray.Manager) {
			a.handleCrash(crashErr, crashedManager)
		},
	}
}

// handleCrash обрабатывает неожиданное падение sing-box.
// Вынесен из замыкания — теперь тестируем и читаем как самостоятельный метод.
func (a *App) handleCrash(crashErr error, crashedManager xray.Manager) {
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

	// BUG FIX: проверяем что crashedManager всё ещё актуален.
	// Если doApply уже заменил менеджер — игнорируем краш старого менеджера.
	if crashedManager != nil && crashedManager != currentMgr {
		a.mainLogger.Info("Игнорируем краш устаревшего менеджера (PID: %d) — активен новый (PID: %d)",
			crashedManager.GetPID(), currentMgr.GetPID())
		return
	}

	lastOut := currentMgr.LastOutput()
	isTunConflict := xray.IsTunConflict(lastOut)

	if isTunConflict {
		wintun.RecordStop()
		// Kill Switch: блокируем трафик на время перезапуска TUN
		if a.cfg.KillSwitch {
			serverIP := a.extractServerIP(a.cfg.SecretFile)
			killswitch.Enable(serverIP, a.mainLogger)
		}

		// Retry loop: пытаемся поднять TUN несколько раз с увеличивающимся gap.
		// Каждая попытка: IncreaseGap → RemoveAdapter → PollUntilFree → Start.
		// Если старт успешен — выходим. Если нет — повторяем.
		const maxTunAttempts = 5
		for attempt := 1; attempt <= maxTunAttempts; attempt++ {
			if a.lifecycleCtx.Err() != nil {
				a.mainLogger.Warn("TUN retry прерван: контекст жизненного цикла отменён")
				a.apiServer.ClearRestarting()
				return
			}
			// Увеличиваем gap один раз при первой попытке (и каждой последующей).
			// На первой попытке — только если краш случился быстро (<30s),
			// иначе gap уже достаточен. При повторах — всегда увеличиваем.
			if attempt > 1 || currentMgr.Uptime() < 30*time.Second {
				wintun.IncreaseAdaptiveGap(a.mainLogger)
			}

			a.mainLogger.Warn("TUN попытка %d/%d — ждём освобождения kernel-объекта...", attempt, maxTunAttempts)
			notification.Send("Proxy", fmt.Sprintf("Перезапуск TUN... (%d/%d)", attempt, maxTunAttempts))

			wintun.RemoveStaleTunAdapter(a.mainLogger)
			a.apiServer.SetRestarting(wintun.EstimateReadyAt())
			a.apiServer.SetTunAttempt(attempt, maxTunAttempts)
			wintun.PollUntilFree(a.lifecycleCtx, a.mainLogger, config.TunInterfaceName)

			a.mainLogger.Info("TUN попытка %d/%d — запускаем sing-box...", attempt, maxTunAttempts)
			// BUG FIX #10: проверяем что менеджер не был заменён другой горутиной
			// (например startBackground) пока шёл PollUntilFree.
			// Запуск на устаревшем менеджере породил бы второй sing-box процесс.
			if a.apiServer.GetXRayManager() != currentMgr {
				a.mainLogger.Warn("XRay менеджер заменён во время TUN retry — прерываем")
				a.apiServer.ClearRestarting()
				return
			}
			if startErr := currentMgr.StartAfterManualCleanup(); startErr != nil {
				a.mainLogger.Error("TUN попытка %d/%d — не удалось запустить: %v", attempt, maxTunAttempts, startErr)
				if attempt == maxTunAttempts {
					a.apiServer.ClearRestarting()
					notification.Send("Proxy — ошибка", fmt.Sprintf("Не удалось поднять TUN после %d попыток", maxTunAttempts))
					_ = a.proxyManager.Disable()
					tray.SetEnabled(false)
					return
				}
				// Записываем краш для следующей итерации
				wintun.RecordStop()
				continue
			}

			// Успех: sing-box запущен — ждём подтверждения что TUN поднялся
			a.apiServer.ClearRestarting()
			killswitch.Disable(a.mainLogger)
			wintun.ResetAdaptiveGap()
			a.mainLogger.Info("sing-box успешно перезапущен (PID: %d, попытка %d/%d)",
				currentMgr.GetPID(), attempt, maxTunAttempts)
			notification.Send("Proxy", fmt.Sprintf("Перезапущен ✓ (попытка %d/%d)", attempt, maxTunAttempts))
			return
		}
		return
	}

	a.mainLogger.Error("sing-box упал неожиданно (%v) — отключаем системный прокси", crashErr)
	notification.Send("Proxy — ошибка", "sing-box упал. Проверьте лог и перезапустите.")
	// BUG FIX #NEW-4: убран блок killswitch.Enable для non-TUN краша.
	// При не-TUN краше перезапуска sing-box не будет — прокси отключается навсегда
	// до ручного рестарта. Включать Kill Switch (блокировать весь трафик) и сразу же
	// отключать его бессмысленно: 4 лишних netsh вызова (~400-1200мс задержки в crash handler)
	// + кратковременное (~50мс) реальное блокирование трафика пользователя.
	// Kill Switch нужен только во время активного retry-цикла TUN (см. isTunConflict блок выше).
	if disableErr := a.proxyManager.Disable(); disableErr != nil {
		a.mainLogger.Error("Не удалось отключить прокси: %v", disableErr)
	} else {
		tray.SetEnabled(false)
		a.mainLogger.Warn("Системный прокси отключён из-за краша sing-box.")
	}
	// Снимаем Kill Switch на случай если он остался активным от предыдущей TUN-попытки.
	// При обычном падении (не конфликт TUN) интернет блокировать не нужно.
	killswitch.Disable(a.mainLogger)
}

// startBackground запускает фоновую инициализацию.
// Wintun cleanup стартует НЕМЕДЛЕННО — параллельно с подъёмом API-сервера.
// Sing-box запускается когда выполнены ОБА условия: apiReady И wintunReady.
func (a *App) startBackground(xrayCfg xray.Config, proxyConfig proxy.Config, apiReady <-chan struct{}) {
	// Wintun cleanup — запускаем сразу, не ждём API.
	// chan error: nil = успех, non-nil = engine download провалился → горутина 2 не стартует sing-box.
	wintunReady := make(chan error, 1)
	go func() {
		// Auto-Engine: скачиваем sing-box.exe если отсутствует.
		// Проверяем до wintun — нет смысла чистить wintun если движка нет.
		if engine.NeedsDownload(a.cfg.SingBoxPath) {
			a.mainLogger.Info("sing-box.exe не найден — автоматическая загрузка...")
			notification.Send("Proxy", "Загружаем sing-box.exe...")

			// Повторяем загрузку до 3 раз: первая попытка может вернуть повреждённый
			// бинарник (0xc0000005 — AV сканирует файл, sing-box.exe удаляется автоматически),
			// при повторе NeedsDownload() снова вернёт true и файл скачается заново.
			const maxDownloadAttempts = 3
			var lastDownloadErr error
			for attempt := 1; attempt <= maxDownloadAttempts; attempt++ {
				if attempt > 1 {
					a.mainLogger.Warn("engine: повторная загрузка (попытка %d/%d)...", attempt, maxDownloadAttempts)
					notification.Send("Proxy", fmt.Sprintf("Повторная загрузка sing-box... (%d/%d)", attempt, maxDownloadAttempts))
					select {
					case <-time.After(2 * time.Second):
					case <-a.lifecycleCtx.Done():
						lastDownloadErr = a.lifecycleCtx.Err()
					}
					if lastDownloadErr != nil {
						break
					}
				}
				progress := make(chan engine.Progress, 20)
				go func() {
					for p := range progress {
						if p.Message != "" {
							a.mainLogger.Info("engine: %s", p.Message)
						}
					}
				}()
				lastDownloadErr = engine.EnsureEngine(a.lifecycleCtx, a.cfg.SingBoxPath, progress)
				close(progress)
				if lastDownloadErr == nil || a.lifecycleCtx.Err() != nil {
					break
				}
				a.mainLogger.Error("engine: попытка %d/%d не удалась: %v", attempt, maxDownloadAttempts, lastDownloadErr)
			}
			if lastDownloadErr != nil {
				a.mainLogger.Error("Не удалось загрузить sing-box.exe: %v", lastDownloadErr)
				notification.Send("Proxy — ошибка", "Не удалось загрузить sing-box. Откройте приложение.")
				wintunReady <- lastDownloadErr
				return
			}
			a.mainLogger.Info("sing-box.exe успешно загружен ✓")
			notification.Send("Proxy", "sing-box.exe загружен ✓")
		}

		a.mainLogger.Info("Фоновая инициализация: wintun cleanup...")

		if _, err := os.Stat(a.cfg.ConfigPath + ".pending"); err == nil {
			_ = os.Remove(a.cfg.ConfigPath + ".pending")
			a.mainLogger.Info("Удалён осиротевший .pending конфиг от предыдущего запуска")
		}

		// ОПТИМИЗАЦИЯ: при чистом завершении (CleanShutdownFile) пропускаем
		// RemoveStaleTunAdapter (taskkill + sc stop + pnputil + reg delete) — ~3-5с.
		// PollUntilFree сразу вернётся (путь 0: gap=0, settle=0).
		// При грязном старте (краш, kill -9) — полная очистка как раньше.
		_, cleanShutdown := os.Stat(wintun.CleanShutdownFile)
		if cleanShutdown != nil {
			// CleanShutdownFile нет → грязный старт — нужна полная очистка
			// ForceDeleteAdapter вызывается внутри RemoveStaleTunAdapter ДО sc stop wintun.
			// После sc stop wintun.dll не может связаться с драйвером → WintunOpenAdapter
			// возвращает NULL не потому что адаптер свободен, а потому что драйвер выгружен.
			// Повторный вызов ForceDeleteAdapter здесь давал ложный true → FastDeleteFile →
			// PollUntilFree использовал 3с settle вместо 60с gap → sing-box стартовал пока
			// stale kernel-объект ещё жив → FATAL "Cannot create a file when that file already exists".
			wintun.RemoveStaleTunAdapter(a.mainLogger)
		} else {
			a.mainLogger.Info("wintun: чистое завершение — пропускаем RemoveStaleTunAdapter")
		}
		wintun.PollUntilFree(a.lifecycleCtx, a.mainLogger, config.TunInterfaceName)
		wintunReady <- nil
	}()

	go func() {
		// Ждём и API, и wintun — оба нужны перед запуском sing-box.
		// Wintun cleanup начался раньше, так что к моменту apiReady
		// часть gap уже может быть отработана.
		<-apiReady
		tray.SetEnabled(false)

		// Ждём вставки VLESS ключа перед генерацией конфига.
		// Иначе на первом запуске с пустым secret.key конфиг может сгенерироваться
		// раньше, чем пользователь успеет вставить ключ во UI, и инициализация завершится.
		a.waitForSecretKey()

		type cfgResult struct {
			err error
		}
		cfgReady := make(chan cfgResult, 1)
		go func() {
			routingCfg, err := config.LoadRoutingConfig(a.cfg.DataDir + "/routing.json")
			if err != nil {
				a.mainLogger.Warn("Не удалось загрузить routing config: %v, используем дефолтный", err)
				routingCfg = config.DefaultRoutingConfig()
			}
			cfgErr := config.GenerateSingBoxConfig(a.cfg.SecretFile, a.cfg.ConfigPath, routingCfg)
			cfgReady <- cfgResult{err: cfgErr}
		}()

		// Ждём завершения обоих: wintun/engine И генерации конфига.
		// Если engine download упал — engineErr != nil, не запускаем sing-box.
		engineErr := <-wintunReady
		cfgRes := <-cfgReady

		if engineErr != nil {
			a.mainLogger.Error("Прерываем запуск sing-box: engine не готов (%v)", engineErr)
			return
		}

		a.mainLogger.Info("Запуск sing-box...")
		if cfgRes.err != nil {
			if !a.handleConfigError(cfgRes.err) {
				return
			}
		}

		// Дополнительная очистка TUN адаптера перед запуском sing-box
		if wintun.ForceDeleteAdapter(config.TunInterfaceName) {
			a.mainLogger.Info("wintun: дополнительная очистка адаптера перед запуском sing-box")
		}

		xrayManager, err := xray.NewManager(xrayCfg, a.lifecycleCtx)
		if err != nil {
			a.mainLogger.Error("Не удалось запустить sing-box: %v", err)
			notification.Send("Proxy — ошибка", "Не удалось запустить sing-box. Проверьте лог.")
			return
		}
		a.mainLogger.Info("Sing-box запущен (PID: %d)", xrayManager.GetPID())
		a.apiServer.SetXRayManager(xrayManager)

		a.mainLogger.Info("Ожидание готовности sing-box на %s...", config.ProxyAddr)
		if err := netutil.WaitForPort(a.lifecycleCtx, config.ProxyAddr, 15*time.Second); err != nil {
			// Если sing-box уже мёртв (например: FATAL[0000] из-за невалидного конфига),
			// включать прокси бессмысленно — на порту никто не слушает.
			// Включаем только если процесс ещё жив (медленный старт, антивирус и т.п.).
			if !xrayManager.IsRunning() {
				a.mainLogger.Error("sing-box завершился с ошибкой конфига или не запустился — прокси не включаем")
				notification.Send("Proxy — ошибка", "sing-box не запустился. Проверьте лог.")
				return
			}
			a.mainLogger.Warn("sing-box не ответил за 15с: %v — включаем прокси всё равно", err)
		} else {
			a.mainLogger.Info("sing-box готов")
		}

		killswitch.Disable(a.mainLogger)
		wintun.ResetAdaptiveGap()
		if err := a.proxyManager.Enable(proxyConfig); err != nil {
			a.mainLogger.Warn("Не удалось включить системный прокси: %v", err)
		}

		// B-2: Запускаем Proxy Guard — восстановление прокси если он был отключен извне
		if a.cfg.ProxyGuardEnabled {
			if err := a.proxyManager.StartGuard(a.lifecycleCtx, a.cfg.ProxyGuardInterval); err != nil {
				a.mainLogger.Warn("Не удалось запустить Proxy Guard: %v", err)
			} else {
				a.apiServer.SetProxyGuardEnabled(true)
				a.mainLogger.Info("Proxy Guard запущен (интервал: %v)", a.cfg.ProxyGuardInterval)
			}
		}

		tray.SetEnabled(true)
		notification.Send("Proxy", "Прокси готов ✓")

		// B-10: запускаем GeoAutoUpdater — проверяет устаревшие geosite файлы
		if a.cfg.GeoAutoUpdateEnabled {
			interval := time.Duration(a.cfg.GeoAutoUpdateIntervalDays) * 24 * time.Hour
			geoUpdater := api.NewGeoAutoUpdater(a.mainLogger, interval)
			a.apiServer.SetGeoAutoUpdater(geoUpdater)
			geoUpdater.Start(a.lifecycleCtx)
			a.mainLogger.Info("B-10: GeoAutoUpdater запущен (интервал: %d дней)", a.cfg.GeoAutoUpdateIntervalDays)
		}

		a.mainLogger.Info("Фоновая инициализация завершена")

		go preWarmProxyConnection(a.lifecycleCtx, config.ProxyAddr, a.cfg.WarmUpHosts, a.mainLogger)

		// Запускаем watchdog + TURN fallback после того как sing-box готов.
		// extractServerIP возвращает "" если ключ не распознан — мониторинг пропускаем.
		if serverIP := a.extractServerIP(a.cfg.SecretFile); serverIP != "" {
			go a.startConnectionMonitor(serverIP)
		}
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

	// Отменяем lifecycleCtx ПЕРВЫМ — прерываем PollUntilFree во всех горутинах
	// (handleCrash, BeforeRestart, startBackground). Без этого API server Shutdown
	// истекает по таймауту пока горутина спит в time.Sleep внутри PollUntilFree.
	if a.lifecycleCancel != nil {
		a.lifecycleCancel()
	}

	if a.apiServer != nil {
		if err := a.apiServer.Shutdown(shutdownCtx); err != nil {
			a.mainLogger.Error("Ошибка при остановке API сервера: %v", err)
		}
		if xrayMgr := a.apiServer.GetXRayManager(); xrayMgr != nil {
			if err := xrayMgr.Stop(); err != nil {
				a.mainLogger.Error("Ошибка при остановке sing-box: %v", err)
			}
			wintun.RecordCleanShutdown()
		}
	}
	if processMonitor != nil {
		processMonitor.Stop()
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

// waitForSecretKey ждёт пока secret.key не будет содержать валидный VLESS URL.
// Нужно для wizard-сценария: движок скачивается ~60с, пользователь ещё не вставил ключ.
// При обычном запуске файл уже есть — возвращается мгновенно.
func (a *App) waitForSecretKey() {
	hasKey := func() bool {
		data, err := os.ReadFile(a.cfg.SecretFile)
		if err != nil || len(data) == 0 {
			return false
		}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") && strings.HasPrefix(line, "vless://") {
				return true
			}
		}
		return false
	}
	if hasKey() {
		return // быстрый путь — ключ уже есть
	}
	a.mainLogger.Info("Ожидание VLESS ключа (wizard mode)...")
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-a.lifecycleCtx.Done():
			return
		case <-ticker.C:
			if hasKey() {
				a.mainLogger.Info("VLESS ключ получен — продолжаем запуск")
				return
			}
		}
	}
}

// extractServerIP читает IP прокси-сервера из secret.key для Kill Switch allowlist.
// При ошибке возвращает пустую строку — Kill Switch всё равно активируется, но только loopback.
// DNS lookup выполняется синхронно только при наличии IP — для hostname используется
// горутина с таймаутом чтобы не блокировать crash handler на десятки секунд.
func (a *App) resetServerIPCache() {
	a.cachedServerIPMu.Lock()
	defer a.cachedServerIPMu.Unlock()
	a.cachedServerIP = ""
	a.cachedForFile = ""
	a.cachedSecretHash = [32]byte{}
}

func (a *App) extractServerIP(secretFile string) string {
	a.cachedServerIPMu.Lock()
	defer a.cachedServerIPMu.Unlock()

	data, _ := os.ReadFile(secretFile)
	currentHash := sha256.Sum256(data)
	if a.cachedServerIP != "" && a.cachedForFile == secretFile && a.cachedSecretHash == currentHash {
		return a.cachedServerIP
	}

	ip := resolveServerIP(a.lifecycleCtx, secretFile)
	a.cachedServerIP = ip
	a.cachedForFile = secretFile
	a.cachedSecretHash = currentHash
	return ip
}

// resolveServerIP читает IP прокси-сервера из secret.key для Kill Switch allowlist.
// При ошибке возвращает пустую строку — Kill Switch всё равно активируется, но только loopback.
// DNS lookup выполняется синхронно только при наличии IP — для hostname используется
// горутина с таймаутом 3с чтобы не блокировать crash handler на десятки секунд.
func extractServerIP(secretFile string) string {
	return resolveServerIP(context.Background(), secretFile)
}

func resolveServerIP(ctx context.Context, secretFile string) string {
	data, err := os.ReadFile(secretFile)
	if err != nil {
		return ""
	}
	// BUG FIX #4: фильтруем комментарии и пустые строки, берём первую валидную.
	// Раньше весь файл обрабатывался как один URL — если в комментарии был '@'
	// (например email), парсинг давал мусорный результат.
	vlessURL := ""
	raw := strings.TrimPrefix(strings.TrimSpace(string(data)), "\xef\xbb\xbf")
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			vlessURL = line
			break
		}
	}
	if vlessURL == "" {
		return ""
	}
	// vless://uuid@host:port?params — извлекаем host
	at := strings.Index(vlessURL, "@")
	if at < 0 {
		return ""
	}
	hostPort := vlessURL[at+1:]
	if q := strings.IndexAny(hostPort, "?#/"); q >= 0 {
		hostPort = hostPort[:q]
	}
	host, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		return hostPort // возможно уже без порта
	}
	// Если уже IP — возвращаем сразу без DNS
	if net.ParseIP(host) != nil {
		return host
	}
	// Hostname: резолвим с таймаутом 3с чтобы не блокировать crash handler.
	// BUG FIX #7: раньше горутина с net.LookupHost не прерывалась при Shutdown —
	// могла висеть до 30с после завершения приложения.
	// net.DefaultResolver.LookupHost с контекстом завершается сам по таймауту/отмене.
	resolveCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupHost(resolveCtx, host)
	if err != nil || len(addrs) == 0 {
		return ""
	}
	return addrs[0]
}

// startConnectionMonitor регистрирует ручное управление TURN из UI.
// Авто-переключение (watchdog → TURN) отключено: сервер использует нестандартный
// порт и DPI-блокировки нет — автоматика не нужна.
// Ручная кнопка в UI по-прежнему работает.
func (a *App) startConnectionMonitor(serverIP string) {
	turnMgr := turnmanager.New(turnmanager.Config{
		Logger: a.mainLogger,
		OnSwitch: func(mode turnmanager.Mode) error {
			if current := a.extractServerIP(a.cfg.SecretFile); current != "" {
				return a.applyTURNMode(mode, current)
			}
			return a.applyTURNMode(mode, serverIP)
		},
	})

	// Ручное управление TURN из UI (кнопка «Обход белых списков»).
	a.apiServer.SetManualTURNFn(func(enabled bool) error {
		if enabled {
			turnMgr.SwitchToTURNManual()
		} else {
			turnMgr.SwitchToDirect()
		}
		a.apiServer.SetTURNManual(enabled)
		return nil
	})

	// Watchdog не запускаем — авто-переключение на TURN отключено.
	// Блокируем горутину до завершения приложения.
	<-a.lifecycleCtx.Done()
	turnMgr.Shutdown()
}

// applyTURNMode переключает sing-box между прямым соединением и TURN туннелем.
//
// Порядок операций при переключении на TURN:
//  1. Останавливаем старый turnproxy если есть (BUG FIX: port conflict).
//  2. Запускаем новый turnproxy на 127.0.0.1:9000.
//  3. Генерируем конфиг sing-box с TURNOverride (VLESS outbound → 127.0.0.1:9000).
//  4. Перезапускаем sing-box — трафик идёт через turnproxy → relay → бэкенд.
//
// При возврате на direct:
//  1. Восстанавливаем конфиг sing-box (без TURNOverride).
//  2. Перезапускаем sing-box.
//  3. Останавливаем turnproxy (он больше не нужен).
func (a *App) applyTURNMode(mode turnmanager.Mode, serverIP string) error {
	a.mainLogger.Info("applyTURNMode: переключение в режим %s", mode)

	if mode == turnmanager.ModeTURN {
		// BUG FIX: останавливаем старый turnproxy перед запуском нового.
		// Без этого при повторном включении TURN (после быстрого direct→TURN→direct→TURN)
		// порт 9000 уже занят предыдущим экземпляром → "bind: Only one usage of each
		// socket address". Это приводило к тому что TURN не включался при второй попытке.
		if a.turnProxy != nil {
			a.mainLogger.Info("applyTURNMode: останавливаем предыдущий turnproxy перед запуском нового")
			a.turnProxy.Stop()
			a.turnProxy = nil
		}

		// Шаг 1: запускаем turnproxy ДО перезапуска sing-box.
		// Relay адрес: тот же IP сервера, настраиваемый порт (default 3478 UDP).
		// Порт 443 UDP — альтернатива если 3478 заблокирован провайдером.
		port := a.cfg.TURNRelayPort
		if port == 0 {
			port = 3478
		}
		relayAddr := fmt.Sprintf("%s:%d", serverIP, port)
		tp := turnproxy.New(turnproxy.Config{
			RelayAddr: relayAddr,
			Logger:    a.mainLogger,
		})
		if err := tp.Start(a.lifecycleCtx); err != nil {
			return fmt.Errorf("turnproxy start: %w", err)
		}
		a.turnProxy = tp
		a.mainLogger.Info("applyTURNMode: turnproxy запущен → relay %s", relayAddr)
	}

	var turnOvr *config.TURNOverride
	if mode == turnmanager.ModeTURN {
		turnOvr = &config.TURNOverride{
			ProxyAddr:    turnmanager.TURNProxyAddr,
			RealServerIP: serverIP,
		}
	}

	routingCfg, err := config.LoadRoutingConfig(a.cfg.DataDir + "/routing.json")
	if err != nil {
		a.mainLogger.Warn("applyTURNMode: не удалось загрузить routing config: %v — используем дефолтный", err)
		routingCfg = config.DefaultRoutingConfig()
	}

	if err := config.GenerateSingBoxConfigWithMode(
		a.cfg.SecretFile, a.cfg.ConfigPath, routingCfg, turnOvr,
	); err != nil {
		// Откатываем turnproxy если конфиг не сгенерировался.
		if a.turnProxy != nil {
			a.turnProxy.Stop()
			a.turnProxy = nil
		}
		return fmt.Errorf("генерация конфига: %w", err)
	}

	// Перезапускаем sing-box с новым конфигом.
	if err := a.apiServer.TriggerRestart(a.cfg.ConfigPath); err != nil {
		if mode == turnmanager.ModeTURN && a.turnProxy != nil {
			a.turnProxy.Stop()
			a.turnProxy = nil
		}
		return err
	}

	// Шаг 3 при возврате на direct: останавливаем turnproxy ПОСЛЕ того как
	// sing-box уже переключён и больше не шлёт трафик на :9000.
	if mode == turnmanager.ModeDirect && a.turnProxy != nil {
		a.turnProxy.Stop()
		a.turnProxy = nil
		a.mainLogger.Info("applyTURNMode: turnproxy остановлен")
	}

	// Уведомляем API сервер о смене режима — фронтенд отобразит индикатор.
	a.apiServer.SetTURNMode(mode == turnmanager.ModeTURN)

	return nil
}

// preWarmProxyConnection устанавливает соединение через HTTP прокси сразу после старта.
// Цель: заставить sing-box открыть первое VLESS/Reality TLS соединение к серверу
// ДО того как пользователь сделает свой первый запрос.
// Без прогрева: первый запрос = DNS + TLS handshake = +100-200мс latency.
// С прогревом: TLS уже открыт, первый запрос идёт через готовое соединение.
//
// BUG FIX #9: google.com нарушал логику при default_action=direct —
// трафик шёл напрямую, не через VLESS, и прогрев не работал.
// Вместо этого используем api.ipify.org — надёжный хост который всегда
// должен идти через proxy-out (он не попадает ни в один прямой маршрут),
// и одновременно проверяем что внешний IP сменился на серверный.
func preWarmProxyConnection(ctx context.Context, proxyAddr string, hosts []string, log logger.Logger) {
	if log == nil {
		log = logger.NewNop()
	}

	if len(hosts) == 0 {
		hosts = []string{
			"https://api.ipify.org?format=json",
			"https://api64.ipify.org?format=json",
			"https://ifconfig.me/ip",
		}
	}

	// Даём TUN интерфейсу время подняться (~2-10с после HTTP-порта).
	// Прогрев через HTTP proxy inbound — не зависит от TUN.
	select {
	case <-ctx.Done():
		return
	case <-time.After(2 * time.Second):
	}

	for _, target := range hosts {
		if target == "" {
			continue
		}

		// Запрос идёт через наш HTTP proxy (127.0.0.1:10807) к известному быстрому хосту.
		// connectproxy.go-style: CONNECT → создаёт TCP через sing-box → VLESS TLS handshake.
		transport := &http.Transport{
			Proxy: func(req *http.Request) (*url.URL, error) {
				return url.Parse("http://" + proxyAddr)
			},
			DialContext: (&net.Dialer{
				Timeout: 10 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 5 * time.Second,
		}
		client := &http.Client{
			Transport: transport,
			Timeout:   15 * time.Second,
		}
		// BUG FIX #9: используем хост который гарантированно идёт через proxy-out.
		// api.ipify.org не входит ни в один direct/block маршрут по умолчанию,
		// поэтому при любом default_action он попадает в proxy-out и открывает
		// VLESS TLS соединение. Дополнительный бонус: видим внешний IP сервера в логе.
		resp, err := client.Get(target)
		if err != nil {
			transport.CloseIdleConnections()
			log.Info("pre-warm: соединение не установлено (%v) — пропускаем", err)
			continue
		}
		// BUG FIX: тело нужно прочитать до конца перед Close() чтобы HTTP transport
		// мог вернуть TCP-соединение в keep-alive пул. Без этого VLESS TLS-соединение
		// не переиспользуется — цель прогрева не достигается.
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		transport.CloseIdleConnections()
		log.Info("pre-warm: VLESS соединение прогрето ✓ (хост %s, статус %d)", target, resp.StatusCode)
		return
	}
	log.Info("pre-warm: не удалось прогреть соединение ни к одному хосту")
}
