//go:build windows

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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"proxyclient/internal/anomalylog"
	"proxyclient/internal/api"
	"proxyclient/internal/config"
	"proxyclient/internal/connhistory"
	"proxyclient/internal/crashreport"
	"proxyclient/internal/engine"
	"proxyclient/internal/eventlog"
	"proxyclient/internal/keepalive"
	"proxyclient/internal/killswitch"
	"proxyclient/internal/latency"
	"proxyclient/internal/logger"
	"proxyclient/internal/netwatch"
	"proxyclient/internal/notification"
	"proxyclient/internal/process"
	"proxyclient/internal/proxy"
	"proxyclient/internal/trafficstats"
	"proxyclient/internal/tray"
	"proxyclient/internal/window"
	"proxyclient/internal/wintun"
	"proxyclient/internal/xray"
)

// AppConfig содержит все настраиваемые параметры приложения.
type AppConfig struct {
	KillSwitch           bool // блокировать трафик при падении туннеля
	ProxyGuardEnabled    bool // B-2: включить Proxy Guard для восстановления системного прокси
	StartProxyOnLaunch   bool // включать прокси сразу после запуска клиента
	ReconnectIntervalMin int
	KeepaliveEnabled     bool
	KeepaliveIntervalSec int
	Schedule             config.Schedule
	MemoryLimitMB        uint64
	// B-10: автообновление geosite баз данных
	GeoAutoUpdateEnabled      bool
	GeoAutoUpdateIntervalDays int
	WarmUpHosts               []string
	SecretFile                string
	SingBoxPath               string
	ConfigPath                string
	RuntimeFile               string
	DataDir                   string
	SettingsFile              string
	AppRulesFile              string
	APIAddress                string
	WebUIURL                  string
	ProxyGuardInterval        time.Duration // B-2: интервал проверки Proxy Guard (default 5s)
}

// DefaultAppConfig возвращает production конфигурацию.
func DefaultAppConfig() AppConfig {
	return AppConfig{
		KillSwitch:                false,
		ProxyGuardEnabled:         true, // B-2: default включить
		StartProxyOnLaunch:        true,
		KeepaliveEnabled:          true,
		KeepaliveIntervalSec:      120,
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
		SettingsFile:       config.AppSettingsFile,
		AppRulesFile:       config.DataDir + "/app_rules.json",
		APIAddress:         config.APIAddress,
		WebUIURL:           "http://localhost:8080",
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
	engineCrashMu    sync.Mutex
	engineCrashCount int
	engineCrashAt    time.Time

	// proxyConfig хранит конфиг прокси для finalizeStartup.
	// Устанавливается startBackground до WaitForSingBoxReady.
	proxyConfig proxy.Config

	// startupOnce гарантирует что finalizeStartup выполняется ровно один раз
	// — либо из startBackground при успешном старте, либо из handleCrash
	// при успешном TUN recovery. Без Once возможен double-enable прокси.
	startupOnce sync.Once
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
	logsDir := filepath.Join(exeDir, "logs")
	_ = os.MkdirAll(logsDir, 0755)

	// Cleanup old .dns_cache.db.old files from previous crashes
	_ = os.Remove(config.DNSCacheFile + ".old")

	anomalyDetector := anomalylog.New(evLog, logsDir)

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
	xrayLogger := eventlog.NewLogger(a.appLogger, a.evLog, "engine")
	return xray.Config{
		ExecutablePath: a.cfg.SingBoxPath,
		ConfigPath:     a.cfg.ConfigPath,
		SecretKeyPath:  a.cfg.SecretFile,
		Args:           []string{"run", "--disable-color"},
		Logger:         xrayLogger,
		SingBoxWriter:  xray.NewFilterWriter(eventlog.NewLineWriter(a.evLog, "sing-box", eventlog.LevelInfo)),
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
			if cleanErr != nil {
				wintun.RecordStop()
				// Передаём ctx — RemoveStaleTunAdapterCtx прерывается если приложение закрывается
				// во время sleep, не зависая на 2+ секунды после отмены lifecycle context.
				wintun.RemoveStaleTunAdapterCtx(ctx, log)
			} else {
				log.Info("wintun: чистое завершение — пропускаем RemoveStaleTunAdapter в BeforeRestart")
			}
			wintun.PollUntilFree(ctx, log, config.TunInterfaceName)
			return nil
		},
		// OnSlowTun: sing-box логирует WARN[0010] "open interface take too much time to finish!"
		// за ~5 секунд до FATAL[0015]. Превентивно увеличиваем adaptive gap — при следующем
		// PollUntilFree gap будет корректным сразу, без ещё одного краша.
		OnSlowTun: func() {
			wintun.IncreaseAdaptiveGap(a.mainLogger)
			a.mainLogger.Warn("wintun: медленный TUN-интерфейс обнаружён — gap увеличен превентивно")
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
		notification.Send("SafeSky — ошибка", "Частые сбои sing-box. Откройте приложение.")
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

	if xray.IsAccessViolation(crashErr) && a.allowEngineAVRetry() {
		a.mainLogger.Warn("sing-box вернул 0xc0000005 — возможно, бинарник ещё проверяется Windows Defender; повторный запуск через 5s")
		addDefenderExclusion(a.cfg.SingBoxPath, a.mainLogger)
		select {
		case <-time.After(5 * time.Second):
		case <-a.lifecycleCtx.Done():
			return
		}
		if a.apiServer.GetXRayManager() != currentMgr {
			a.mainLogger.Info("sing-box менеджер заменён во время ожидания AV retry — прерываем")
			return
		}
		if err := currentMgr.Start(); err != nil {
			a.mainLogger.Error("Не удалось повторно запустить sing-box после 0xc0000005: %v", err)
		} else {
			a.mainLogger.Info("Повторный запуск sing-box после 0xc0000005 выполнен")
			return
		}
	}

	lastOut := currentMgr.LastOutput()
	if path, err := crashreport.Generate(lastOut, crashErr.Error(), a.cfg.ConfigPath, currentMgr.MemoryMB()).SaveToFile(); err == nil {
		a.mainLogger.Info("Краш-отчёт сохранён: %s", path)
	}
	isTunConflict := xray.IsTunConflict(lastOut)

	// FIX 3: обработка "initialize cache-file: timeout" — удаляем зависший dns_cache.db и перезапускаем.
	//
	// BUG FIX: предыдущая реализация пыталась os.Remove → os.Rename, но оба вызова
	// проваливались когда другой (zombie) sing-box процесс держал файл. Результат:
	// бесконечный цикл краш → cache timeout → не удалось удалить → рестарт → cache timeout.
	//
	// Новый алгоритм:
	//  1. taskkill /F /IM sing-box.exe — убиваем ВСЕ sing-box, включая orphaned процессы
	//  2. Пауза 500ms — даём Windows освободить file handles
	//  3. os.Remove с retry (3 попытки × 1s) — файл теперь гарантированно не заблокирован
	//  4. StartAfterManualCleanup — чистый старт с пустым кэшем
	isCacheTimeout := xray.IsCacheFileTimeout(lastOut)
	if isCacheTimeout {
		a.mainLogger.Warn("sing-box: cache-file timeout — убиваем все sing-box и удаляем dns_cache.db")

		// Шаг 1: убиваем ВСЕ процессы sing-box чтобы освободить lock на dns_cache.db.
		// taskkill /F /IM — по имени образа, убивает все экземпляры включая orphaned.
		killCmd := exec.CommandContext(a.lifecycleCtx, "taskkill", "/F", "/IM", "sing-box.exe")
		killCmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
		if killOut, killErr := killCmd.CombinedOutput(); killErr != nil {
			// Ошибка ожидаема если нет запущенных sing-box (уже все умерли)
			a.mainLogger.Info("taskkill sing-box: %v (%s)", killErr, strings.TrimSpace(string(killOut)))
		} else {
			a.mainLogger.Info("taskkill sing-box: все процессы убиты")
		}

		// Шаг 2: даём Windows время освободить file handles после убийства процессов
		time.Sleep(500 * time.Millisecond)

		// Шаг 3: удаляем dns_cache.db с retry
		const cacheRetries = 3
		for i := 1; i <= cacheRetries; i++ {
			if err := os.Remove(config.DNSCacheFile); err == nil || os.IsNotExist(err) {
				a.mainLogger.Info("dns_cache.db удалён успешно")
				break
			} else if i < cacheRetries {
				a.mainLogger.Warn("dns_cache.db всё ещё заблокирован (попытка %d/%d): %v", i, cacheRetries, err)
				time.Sleep(1 * time.Second)
			} else {
				// Последняя попытка — пробуем rename как fallback
				a.mainLogger.Warn("dns_cache.db: не удалось удалить за %d попыток, пробуем rename", cacheRetries)
				tmpPath := config.DNSCacheFile + ".old"
				_ = os.Remove(tmpPath)
				if renErr := os.Rename(config.DNSCacheFile, tmpPath); renErr != nil {
					a.mainLogger.Error("dns_cache.db: не удалось ни удалить ни переименовать: %v", renErr)
				}
			}
		}

		// Шаг 4: очистка wintun kernel-объекта ПЕРЕД перезапуском.
		//
		// BUG FIX: taskkill убивает sing-box принудительно — wintun не освобождает
		// TUN adapter через WintunCloseAdapter. Kernel-объект \\Device\\WINTUN-{GUID}
		// остаётся жить. StartAfterManualCleanup (без BeforeRestart/PollUntilFree) →
		// новый sing-box пытается создать адаптер → FATAL "Cannot create a file when
		// that file already exists".
		//
		// Решение: RecordStop + RemoveStaleTunAdapterCtx + PollUntilFree ПЕРЕД стартом.
		// Аналогично TUN-conflict пути, но без увеличения adaptive gap
		// (cache-timeout ≠ slow TUN — gap трогать не нужно).
		a.mainLogger.Info("sing-box: cache-timeout recovery — очистка wintun перед перезапуском...")
		wintun.RecordStop()
		wintun.RemoveStaleTunAdapterCtx(a.lifecycleCtx, a.mainLogger)
		a.apiServer.SetRestarting(wintun.EstimateReadyAt())
		wintun.PollUntilFree(a.lifecycleCtx, a.mainLogger, config.TunInterfaceName)

		// Шаг 5: перезапуск sing-box с чистым кэшем (cleanup выполнен выше вручную).
		if a.apiServer.GetXRayManager() != currentMgr {
			a.mainLogger.Warn("sing-box менеджер заменён во время cache-timeout recovery — прерываем")
			a.apiServer.ClearRestarting()
			return
		}
		if startErr := currentMgr.StartAfterManualCleanup(); startErr != nil {
			a.apiServer.ClearRestarting()
			a.mainLogger.Error("Не удалось перезапустить sing-box после cache-file timeout: %v", startErr)
			_ = a.proxyManager.Disable()
			tray.SetEnabled(false)
			return
		}
		a.apiServer.ClearRestarting()
		return
	}

	if isTunConflict {
		wintun.RecordStop()
		tray.SetWarming(true)
		// Kill Switch: блокируем трафик на время перезапуска TUN.
		// defer гарантирует снятие правил при любом return из функции:
		// успех, все попытки провалены, менеджер заменён, контекст отменён.
		if a.cfg.KillSwitch {
			serverIP := a.extractServerIP(a.cfg.SecretFile)
			killswitch.Enable(serverIP, a.mainLogger)
			defer killswitch.Disable(a.mainLogger)
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
			// БАГ 6: обновляем timestamp стоп-файла перед каждой попыткой.
			// EstimateReadyAt() вычисляет ETA от mtime стоп-файла — при повторных попытках
			// старый timestamp даёт неверный ETA (может быть в далёком прошлом).
			wintun.RecordStop()
			// Увеличиваем gap: при первой попытке — только если краш быстрый (<30s),
			// при быстром краше gap уже корректен и увеличивать преждевременно.
			// При повторных попытках — всегда: предыдущая попытка не помогла.
			if attempt == 1 && currentMgr.Uptime() >= 30*time.Second {
				// Долгоживущий процесс упал не из-за TUN таймаута — gap не трогаем.
			} else {
				wintun.IncreaseAdaptiveGap(a.mainLogger)
			}

			a.mainLogger.Warn("TUN попытка %d/%d — ждём освобождения kernel-объекта...", attempt, maxTunAttempts)
			notification.Send("SafeSky", fmt.Sprintf("Перезапуск TUN... (%d/%d)", attempt, maxTunAttempts))

			wintun.RemoveStaleTunAdapterCtx(a.lifecycleCtx, a.mainLogger)
			a.apiServer.SetRestarting(wintun.EstimateReadyAt())
			a.apiServer.SetTunAttempt(attempt, maxTunAttempts)
			wintun.PollUntilFree(a.lifecycleCtx, a.mainLogger, config.TunInterfaceName)

			a.mainLogger.Info("TUN попытка %d/%d — запускаем sing-box...", attempt, maxTunAttempts)
			// BUG FIX #10: проверяем что менеджер не был заменён другой горутиной
			// (например startBackground) пока шёл PollUntilFree.
			// Запуск на устаревшем менеджере породил бы второй sing-box процесс.
			if a.apiServer.GetXRayManager() != currentMgr {
				a.mainLogger.Warn("sing-box менеджер заменён во время TUN retry — прерываем")
				a.apiServer.ClearRestarting()
				return
			}
			// Превентивное удаление dns_cache.db: wintun crash recovery делает taskkill —
			// dns_cache.db может остаться заблокированным, новый старт упадёт с cache timeout.
			_ = os.Remove(config.DNSCacheFile)
			if startErr := currentMgr.StartAfterManualCleanup(); startErr != nil {
				a.mainLogger.Error("TUN попытка %d/%d — не удалось запустить: %v", attempt, maxTunAttempts, startErr)
				if attempt == maxTunAttempts {
					a.apiServer.ClearRestarting()
					notification.Send("SafeSky — ошибка", fmt.Sprintf("Не удалось поднять TUN после %d попыток", maxTunAttempts))
					_ = a.proxyManager.Disable()
					tray.SetWarming(false)
					tray.SetEnabled(false)
					return
				}
				// Записываем краш для следующей итерации
				wintun.RecordStop()
				continue
			}

			// Успех: sing-box запущен — ждём подтверждения что TUN поднялся
			a.apiServer.ClearRestarting()
			// killswitch.Disable вызывается через defer выше — не дублируем
			wintun.ResetAdaptiveGap()
			a.mainLogger.Info("sing-box успешно перезапущен (PID: %d, попытка %d/%d)",
				currentMgr.GetPID(), attempt, maxTunAttempts)
			notification.Send("SafeSky", fmt.Sprintf("Перезапущен ✓ (попытка %d/%d)", attempt, maxTunAttempts))
			// BUG FIX: при первом запуске startBackground мог выйти без включения прокси
			// (WaitForSingBoxReady тайм-аут истёк до завершения TUN recovery).
			// startupOnce гарантирует что включаем прокси ровно один раз.
			a.startupOnce.Do(a.finalizeStartup)
			return
		}
		return
	}

	a.mainLogger.Error("sing-box упал неожиданно (%v) — отключаем системный прокси", crashErr)
	notification.Send("SafeSky — ошибка", "sing-box упал. Проверьте лог и перезапустите.")
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

func addDefenderExclusion(path string, log logger.Logger) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return
	}
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", "Add-MpPreference -ExclusionPath $env:SAFESKY_DEFENDER_EXCLUSION")
	cmd.Env = append(os.Environ(), "SAFESKY_DEFENDER_EXCLUSION="+absPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Run(); err != nil {
		if log != nil {
			log.Debug("не удалось добавить исключение Windows Defender: %v", err)
		}
	}
}

func (a *App) allowEngineAVRetry() bool {
	a.engineCrashMu.Lock()
	defer a.engineCrashMu.Unlock()

	now := time.Now()
	if a.engineCrashAt.IsZero() || now.Sub(a.engineCrashAt) > 3*time.Minute {
		a.engineCrashAt = now
		a.engineCrashCount = 0
	}
	if a.engineCrashCount >= 5 {
		return false
	}
	a.engineCrashCount++
	return true
}

// finalizeStartup выполняет post-startup логику: включение прокси, Proxy Guard,
// GeoAutoUpdater, tray и pre-warm соединения.
// Вызывается строго через startupOnce.Do — ровно один раз, либо из startBackground
// при успешном старте, либо из handleCrash при успешном TUN recovery.
func (a *App) finalizeStartup() {
	// BUG FIX: если приложение уже завершается (пользователь вышел через трей во время
	// TUN recovery), не включаем прокси и не показываем уведомление "Прокси готов".
	// Без этой проверки startupOnce.Do вызывается, lifecycleCancel() уже отработал,
	// но notification.Send ещё в очереди горутин — пользователь видит "Прокси готов ✓"
	// уже после закрытия приложения, а системный прокси остаётся включённым.
	if a.lifecycleCtx.Err() != nil {
		a.mainLogger.Info("finalizeStartup: контекст отменён — пропускаем включение прокси и уведомление")
		return
	}

	tray.SetWarming(false)
	killswitch.Disable(a.mainLogger)
	wintun.ResetAdaptiveGap()

	proxyEnabled := false
	if a.cfg.StartProxyOnLaunch {
		if err := a.proxyManager.Enable(a.proxyConfig); err != nil {
			a.mainLogger.Warn("Не удалось включить системный прокси: %v", err)
		} else {
			proxyEnabled = true
			setClashMode(a.lifecycleCtx, a.mainLogger, "rule")
		}
	} else {
		if err := a.proxyManager.Disable(); err != nil {
			a.mainLogger.Warn("Не удалось оставить прокси выключенным при старте: %v", err)
		}
		setClashMode(a.lifecycleCtx, a.mainLogger, "direct")
		a.mainLogger.Info("Автовключение прокси отключено — ожидаем нажатия кнопки включения")
	}

	// B-2: Proxy Guard
	a.apiServer.SetProxyGuardInterval(a.cfg.ProxyGuardInterval)
	if a.cfg.ProxyGuardEnabled {
		if err := a.proxyManager.StartGuard(a.lifecycleCtx, a.cfg.ProxyGuardInterval); err != nil {
			a.mainLogger.Warn("Не удалось запустить Proxy Guard: %v", err)
		} else {
			a.apiServer.SetProxyGuardEnabled(true)
			a.mainLogger.Info("Proxy Guard запущен (интервал: %v)", a.cfg.ProxyGuardInterval)
		}
	}

	tray.SetEnabled(proxyEnabled)
	go a.refreshTrayServers(a.cfg.APIAddress)
	if proxyEnabled {
		notification.Send("SafeSky", "Прокси готов ✓")
	} else {
		notification.Send("SafeSky", "Прокси готов к ручному включению")
	}

	// B-10: GeoAutoUpdater
	if a.cfg.GeoAutoUpdateEnabled {
		interval := time.Duration(a.cfg.GeoAutoUpdateIntervalDays) * 24 * time.Hour
		geoUpdater := api.NewGeoAutoUpdater(a.mainLogger, interval)
		a.apiServer.SetGeoAutoUpdater(geoUpdater)
		geoUpdater.Start(a.lifecycleCtx)
		a.mainLogger.Info("B-10: GeoAutoUpdater запущен (интервал: %d дней)", a.cfg.GeoAutoUpdateIntervalDays)
	}

	if mgr := a.apiServer.GetXRayManager(); mgr != nil {
		mgr.SetHealthAlertFn(func() {
			a.mainLogger.Info("HealthAlert: деградация — запуск автоподключения")
			connhistory.Global.Add(connhistory.Event{Time: time.Now(), Kind: connhistory.EventFailover, Reason: "health alert"})
			a.apiServer.AutoConnect()
		})
	}
	if a.cfg.ReconnectIntervalMin > 0 {
		a.apiServer.StartPeriodicReconnect(time.Duration(a.cfg.ReconnectIntervalMin) * time.Minute)
	}
	if a.cfg.KeepaliveEnabled {
		interval := time.Duration(a.cfg.KeepaliveIntervalSec) * time.Second
		if interval <= 0 {
			interval = 120 * time.Second
		}
		go keepalive.Run(a.lifecycleCtx, config.ProxyAddr, interval)
	}
	go a.runNetwatch()
	go a.runTrayHealth()
	go a.runMemoryWatchdog()
	if a.cfg.Schedule.Enabled {
		go a.runScheduler()
	}

	a.mainLogger.Info("Фоновая инициализация завершена")
	a.apiServer.DrainQueuedApply()
	if proxyEnabled {
		go preWarmProxyConnection(a.lifecycleCtx, config.ProxyAddr, a.cfg.WarmUpHosts, a.mainLogger)
	}
	go a.runLatencyHistory()
}

func (a *App) runNetwatch() {
	var mu sync.Mutex
	var lastEvent time.Time
	var lastApply time.Time
	startedAt := time.Now()
	const (
		startupQuiet  = 30 * time.Second
		debounceDelay = 5 * time.Second
		applyCooldown = 90 * time.Second
	)
	if err := netwatch.Watch(a.lifecycleCtx, func() {
		now := time.Now()
		if now.Sub(startedAt) < startupQuiet {
			return
		}
		mu.Lock()
		if now.Sub(lastEvent) < debounceDelay {
			mu.Unlock()
			return
		}
		lastEvent = now
		mu.Unlock()

		time.Sleep(debounceDelay)
		if a.lifecycleCtx.Err() != nil {
			return
		}
		if a.apiServer.IsRestarting() || a.apiServer.IsWarming() || a.apiServer.IsApplyBusy() {
			a.mainLogger.Debug("Netwatch: событие сети пропущено — sing-box/apply ещё стабилизируется")
			return
		}
		mu.Lock()
		if time.Since(lastApply) < applyCooldown {
			mu.Unlock()
			a.mainLogger.Debug("Netwatch: событие сети пропущено — cooldown после предыдущего apply")
			return
		}
		lastApply = time.Now()
		mu.Unlock()

		a.mainLogger.Info("Netwatch: смена сети — переприменяем конфиг")
		connhistory.Global.Add(connhistory.Event{Time: time.Now(), Kind: connhistory.EventNetChange})
		if err := a.apiServer.TriggerApply(); err != nil {
			a.mainLogger.Warn("Netwatch TriggerApply: %v", err)
		}
	}); err != nil && a.lifecycleCtx.Err() == nil {
		a.mainLogger.Warn("Netwatch stopped: %v", err)
	}
}

func (a *App) runTrayHealth() {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-a.lifecycleCtx.Done():
			return
		case <-t.C:
			mgr := a.apiServer.GetXRayManager()
			if mgr == nil {
				continue
			}
			cnt, _, alert := mgr.GetHealthStatus()
			switch {
			case alert:
				tray.SetHealthState(tray.HealthCritical)
			case cnt > 0:
				tray.SetHealthState(tray.HealthDegraded)
			default:
				tray.SetHealthState(tray.HealthOK)
			}
		}
	}
}

func (a *App) runMemoryWatchdog() {
	if a.cfg.MemoryLimitMB == 0 {
		return
	}
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-a.lifecycleCtx.Done():
			return
		case <-t.C:
			mgr := a.apiServer.GetXRayManager()
			if mgr == nil {
				continue
			}
			used := mgr.MemoryMB()
			if used > a.cfg.MemoryLimitMB {
				a.mainLogger.Warn("sing-box: %d MB > лимит %d MB — мягкий перезапуск", used, a.cfg.MemoryLimitMB)
				connhistory.Global.Add(connhistory.Event{Time: time.Now(), Kind: connhistory.EventReconnect, Reason: fmt.Sprintf("memory limit %dMB", used)})
				_ = a.apiServer.TriggerApply()
			}
		}
	}
}

func (a *App) runScheduler() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-a.lifecycleCtx.Done():
			return
		case <-t.C:
			shouldBeOn := api.IsWithinSchedule(time.Now(), a.cfg.Schedule)
			if shouldBeOn && !a.proxyManager.IsEnabled() {
				_ = a.proxyManager.Enable(a.proxyConfig)
				setClashMode(a.lifecycleCtx, a.mainLogger, "rule")
			} else if !shouldBeOn && a.proxyManager.IsEnabled() {
				_ = a.proxyManager.Disable()
				setClashMode(a.lifecycleCtx, a.mainLogger, "direct")
			}
		}
	}
}

func (a *App) runLatencyHistory() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	type pingAllResp struct {
		Results []struct {
			ID        string `json:"id"`
			LatencyMs int64  `json:"latency_ms"`
			OK        bool   `json:"ok"`
		} `json:"results"`
	}
	client := &http.Client{Timeout: 20 * time.Second}
	for {
		select {
		case <-a.lifecycleCtx.Done():
			return
		case <-t.C:
			req, _ := http.NewRequestWithContext(a.lifecycleCtx, http.MethodGet, "http://localhost"+a.cfg.APIAddress+"/api/servers/ping-all", nil)
			resp, err := client.Do(req)
			if err != nil {
				continue
			}
			var out pingAllResp
			err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out)
			resp.Body.Close()
			if err != nil {
				continue
			}
			for _, result := range out.Results {
				ms := result.LatencyMs
				if !result.OK {
					ms = -1
				}
				latency.Global.Record(result.ID, ms)
			}
		}
	}
}

// startBackground запускает фоновую инициализацию.
// Wintun cleanup стартует НЕМЕДЛЕННО — параллельно с подъёмом API-сервера.
// Sing-box запускается когда выполнены ОБА условия: apiReady И wintunReady.
func (a *App) startBackground(xrayCfg xray.Config, proxyConfig proxy.Config, apiReady <-chan struct{}) {
	// Wintun cleanup — запускаем сразу, не ждём API.
	// err != nil = engine download провалился → горутина 2 не стартует sing-box.
	type startupPrepResult struct {
		err        error
		dirtyStart bool
	}
	wintunReady := make(chan startupPrepResult, 1)
	type cfgResult struct {
		err error
	}
	cfgReady := make(chan cfgResult, 1)
	sendWintunReady := func(result startupPrepResult) bool {
		select {
		case wintunReady <- result:
			return true
		case <-a.lifecycleCtx.Done():
			return false
		}
	}
	sendCfgReady := func(result cfgResult) bool {
		select {
		case cfgReady <- result:
			return true
		case <-a.lifecycleCtx.Done():
			return false
		}
	}

	// Конфиг не зависит от API и Wintun, поэтому готовим его параллельно.
	go func() {
		a.waitForSecretKey()
		if a.lifecycleCtx.Err() != nil {
			sendCfgReady(cfgResult{err: a.lifecycleCtx.Err()})
			return
		}
		routingCfg, err := config.LoadRoutingConfig(a.cfg.DataDir + "/routing.json")
		if err != nil {
			a.mainLogger.Warn("Не удалось загрузить routing config: %v, используем дефолтный", err)
			routingCfg = config.DefaultRoutingConfig()
		}
		if appSettings, err := config.LoadAppSettings(a.cfg.SettingsFile); err == nil && appSettings.ManualSingBoxConfig {
			if _, statErr := os.Stat(a.cfg.ConfigPath); statErr == nil {
				a.mainLogger.Info("Ручной sing-box конфиг включён — стартуем с существующим %s", a.cfg.ConfigPath)
				sendCfgReady(cfgResult{})
				return
			}
			a.mainLogger.Warn("Ручной sing-box конфиг включён, но %s не найден — генерируем заново", a.cfg.ConfigPath)
		}
		sendCfgReady(cfgResult{err: config.GenerateSingBoxConfig(a.cfg.SecretFile, a.cfg.ConfigPath, routingCfg)})
	}()

	go func() {
		// Auto-Engine: скачиваем sing-box.exe если отсутствует.
		// Проверяем до wintun — нет смысла чистить wintun если движка нет.
		if engine.NeedsDownload(a.cfg.SingBoxPath) {
			a.mainLogger.Info("sing-box.exe не найден — автоматическая загрузка...")
			notification.Send("SafeSky", "Загружаем sing-box.exe...")

			// Повторяем загрузку до 3 раз: первая попытка может вернуть повреждённый
			// бинарник (0xc0000005 — AV сканирует файл, sing-box.exe удаляется автоматически),
			// при повторе NeedsDownload() снова вернёт true и файл скачается заново.
			const maxDownloadAttempts = 3
			var lastDownloadErr error
			for attempt := 1; attempt <= maxDownloadAttempts; attempt++ {
				if attempt > 1 {
					a.mainLogger.Warn("engine: повторная загрузка (попытка %d/%d)...", attempt, maxDownloadAttempts)
					notification.Send("SafeSky", fmt.Sprintf("Повторная загрузка sing-box... (%d/%d)", attempt, maxDownloadAttempts))
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
				// При отмене контекста (пользователь закрыл приложение) — не показываем ошибку.
				if errors.Is(lastDownloadErr, context.Canceled) || errors.Is(lastDownloadErr, context.DeadlineExceeded) {
					sendWintunReady(startupPrepResult{err: lastDownloadErr})
					return
				}
				a.mainLogger.Error("Не удалось загрузить sing-box.exe: %v", lastDownloadErr)
				notification.Send("SafeSky — ошибка", "Не удалось загрузить sing-box. Проверьте интернет и перезапустите.")
				sendWintunReady(startupPrepResult{err: lastDownloadErr})
				return
			}
			a.mainLogger.Info("sing-box.exe успешно загружен ✓")
			notification.Send("SafeSky", "sing-box.exe загружен ✓")
		}

		a.mainLogger.Info("Фоновая инициализация: wintun cleanup...")

		if _, err := os.Stat(a.cfg.ConfigPath + ".pending"); err == nil {
			_ = os.Remove(a.cfg.ConfigPath + ".pending")
			a.mainLogger.Info("Удалён осиротевший .pending конфиг от предыдущего запуска")
		}

		// БАГ 9: удаляем устаревший wintun_stopped_at если он старше 24ч.
		// После перезагрузки Windows или длительного простоя наносекундная метка
		// из прошлого сеанса вычисляет нереалистично большой gap (дни, не секунды).
		// PollUntilFree в этом случае должен использовать dirty-путь, а не gap от старой метки.
		if fi, err := os.Stat(wintun.StopFile); err == nil {
			if time.Since(fi.ModTime()) > 24*time.Hour {
				a.mainLogger.Info("wintun: удаляем устаревший wintun_stopped_at (mtime=%s)", fi.ModTime().Format(time.RFC3339))
				_ = os.Remove(wintun.StopFile)
			}
		}

		// ОПТИМИЗАЦИЯ: при чистом завершении (CleanShutdownFile) пропускаем
		// RemoveStaleTunAdapter (taskkill + sc stop + pnputil + reg delete) — ~3-5с.
		// PollUntilFree сразу вернётся (путь 0: gap=0, settle=0).
		// При грязном старте (краш, kill -9) — полная очистка как раньше.
		_, cleanShutdown := os.Stat(wintun.CleanShutdownFile)
		dirtyStart := cleanShutdown != nil
		if cleanShutdown != nil {
			if _, stopErr := os.Stat(wintun.StopFile); os.IsNotExist(stopErr) &&
				!wintun.StartupCleanupNeeded(a.lifecycleCtx, a.mainLogger, config.TunInterfaceName) {
				dirtyStart = false
			} else {
				// CleanShutdownFile нет → грязный старт — нужна полная очистка
				// ForceDeleteAdapter вызывается внутри RemoveStaleTunAdapter ДО sc stop wintun.
				// После sc stop wintun.dll не может связаться с драйвером → WintunOpenAdapter
				// возвращает NULL не потому что адаптер свободен, а потому что драйвер выгружен.
				// Повторный вызов ForceDeleteAdapter здесь давал ложный true → FastDeleteFile →
				// PollUntilFree использовал 3с settle вместо 60с gap → sing-box стартовал пока
				// stale kernel-объект ещё жив → FATAL "Cannot create a file when that file already exists".
				wintun.RemoveStaleTunAdapterCtx(a.lifecycleCtx, a.mainLogger)
			}
		} else {
			a.mainLogger.Info("wintun: чистое завершение — пропускаем RemoveStaleTunAdapter")
		}
		wintun.PollUntilFree(a.lifecycleCtx, a.mainLogger, config.TunInterfaceName)
		sendWintunReady(startupPrepResult{dirtyStart: dirtyStart})
	}()

	go func() {
		// Ждём и API, и wintun — оба нужны перед запуском sing-box.
		// Wintun cleanup начался раньше, так что к моменту apiReady
		// часть gap уже может быть отработана.
		select {
		case <-apiReady:
		case <-a.lifecycleCtx.Done():
			return
		}
		tray.SetEnabled(false)
		tray.SetWarming(true)

		// Сохраняем proxyConfig чтобы handleCrash мог использовать его при TUN recovery.
		a.proxyConfig = proxyConfig

		// Ждём завершения обоих: wintun/engine И генерации конфига.
		// Если engine download упал — engineErr != nil, не запускаем sing-box.
		var prep startupPrepResult
		select {
		case prep = <-wintunReady:
		case <-a.lifecycleCtx.Done():
			return
		}
		if prep.err != nil {
			a.mainLogger.Error("Прерываем запуск sing-box: engine не готов (%v)", prep.err)
			return
		}
		var cfgRes cfgResult
		select {
		case cfgRes = <-cfgReady:
		case <-a.lifecycleCtx.Done():
			return
		}

		a.mainLogger.Info("Запуск sing-box...")
		if cfgRes.err != nil {
			if !a.handleConfigError(cfgRes.err) {
				return
			}
		}

		// DNS cache сохраняем на чистом старте: это ускоряет первые DNS-ответы после запуска.
		// Удаляем только после грязного завершения/cleanup, когда файл мог остаться заблокированным.
		if prep.dirtyStart {
			if removeErr := os.Remove(config.DNSCacheFile); removeErr == nil {
				a.mainLogger.Info("dns_cache.db удалён после грязного старта")
			}
		}

		if busyPorts := checkPortsBusy(config.ProxyAddr, config.ClashAPIAddr); len(busyPorts) > 0 {
			a.mainLogger.Error("Порты заняты другим процессом: %v — sing-box не запустится", busyPorts)
			notification.Send("SafeSky — ошибка", fmt.Sprintf("Порт %d занят. Завершите конфликтующий процесс.", busyPorts[0]))
			return
		}

		xrayManager, err := xray.NewManager(xrayCfg, a.lifecycleCtx)
		if err != nil {
			a.mainLogger.Error("Не удалось запустить sing-box: %v", err)
			notification.Send("SafeSky — ошибка", "Не удалось запустить sing-box. Проверьте лог.")
			return
		}
		a.mainLogger.Info("Sing-box запущен (PID: %d)", xrayManager.GetPID())
		a.apiServer.SetXRayManager(xrayManager)

		// BUG FIX #5: заменяем WaitForPort (TCP-пробинг 127.0.0.1:10807, 15s) на
		// WaitForSingBoxReady (HTTP-поллинг Clash API :9090, 60s).
		// WaitForPort не гарантировал готовность TUN — порт 10807 мог открыться раньше
		// чем wintun завершил инициализацию интерфейса ("open interface take too much time").
		// WaitForSingBoxReady опрашивает тот же endpoint что и doApply — единая точка проверки.
		a.mainLogger.Info("Ожидание готовности sing-box (Clash API)...")
		if err := api.WaitForSingBoxReady(a.lifecycleCtx, a.mainLogger); err != nil {
			// Если sing-box уже мёртв (например: FATAL[0000] из-за невалидного конфига),
			// включать прокси бессмысленно — на порту никто не слушает.
			// Включаем только если процесс ещё жив (медленный старт, антивирус и т.п.).
			if !xrayManager.IsRunning() {
				// BUG FIX: если doApply заменил менеджер (TriggerApplyFull / смена сервера),
				// наш xrayManager M1 был остановлен намеренно. Это нормально — не ошибка.
				// doApply создал новый менеджер M2 и управляет им самостоятельно.
				// Не логируем ERROR, не отправляем уведомление.
				if a.apiServer.GetXRayManager() != xrayManager {
					a.mainLogger.Info("sing-box завершился — управление передано doApply (менеджер заменён)")
					// finalizeStartup вызовет doApply при успехе или handleCrash при TUN recovery.
					return
				}
				// BUG FIX: если apiServer.IsRestarting() — handleCrash выполняет TUN recovery.
				// Не логируем ERROR и не отправляем уведомление об ошибке: handleCrash
				// сам перезапустит sing-box и вызовет finalizeStartup при успехе.
				// Раньше пользователь видел пугающее "sing-box не запустился" в логах-аномалиях
				// хотя TUN recovery успешно завершался через 30-120с.
				if a.apiServer.IsRestarting() {
					a.mainLogger.Info("sing-box завершился — TUN recovery в процессе (handleCrash)")
					return
				}
				a.mainLogger.Error("sing-box завершился с ошибкой конфига или не запустился — прокси не включаем")
				notification.Send("SafeSky — ошибка", "sing-box не запустился. Проверьте лог.")
				// НЕ вызываем finalizeStartup — handleCrash вызовет её при успешном TUN recovery.
				return
			}
			a.mainLogger.Warn("sing-box не ответил за 60с: %v — включаем прокси всё равно", err)
		} else {
			a.mainLogger.Info("sing-box готов")
		}
		a.startupOnce.Do(a.finalizeStartup)

	}()
}

func checkPortsBusy(addrs ...string) []int {
	var busy []int
	for _, addr := range addrs {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			_, port, splitErr := net.SplitHostPort(addr)
			if splitErr == nil {
				if p, atoiErr := strconv.Atoi(port); atoiErr == nil {
					busy = append(busy, p)
				}
			}
			continue
		}
		_ = ln.Close()
	}
	return busy
}

// handleConfigError обрабатывает ошибку генерации конфига.
// Возвращает true если можно продолжить (запустить с существующим конфигом).
func (a *App) handleConfigError(err error) bool {
	secretData, readErr := config.ReadSecretKey(a.cfg.SecretFile)
	if readErr != nil || strings.TrimSpace(secretData) == "" {
		a.mainLogger.Error("файл %s не найден или пуст — вставьте вашу VLESS-ссылку", a.cfg.SecretFile)
		notification.Send("SafeSky — ошибка", "Вставьте VLESS-ссылку в secret.key")
		return false
	}
	allComments := true
	for _, line := range strings.Split(strings.TrimSpace(secretData), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			allComments = false
			break
		}
	}
	if allComments {
		a.mainLogger.Error("файл %s содержит только комментарии — вставьте вашу VLESS-ссылку", a.cfg.SecretFile)
		notification.Send("SafeSky — ошибка", "Вставьте VLESS-ссылку в secret.key")
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
			// BUG FIX: RecordCleanShutdown() перенесён в OnGracefulStop callback.
			// Раньше RecordCleanShutdown() вызывался здесь ВСЕГДА — даже если sing-box
			// был убит принудительно (Kill) или находился в crash-recovery состоянии.
			// Результат: CleanShutdownFile создавался при грязном завершении →
			// следующий старт пропускал RemoveStaleTunAdapterCtx → stale wintun kernel
			// объект оставался → FATAL "Cannot create a file when that file already exists".
			//
			// Правильно: CleanShutdownFile пишется ТОЛЬКО OnGracefulStop (CTRL+BREAK
			// graceful path), что гарантирует sing-box корректно закрыл WintunCloseAdapter.
			// При Kill или краше файл НЕ создаётся → следующий старт выполнит полную очистку.
		}
	}
	if processMonitor != nil {
		processMonitor.Stop()
	}

	// BUG FIX #18: RecordStop() удаляет CleanShutdownFile — не вызываем его
	// при штатном завершении, потому что RecordCleanShutdown() записан
	// через OnGracefulStop при CTRL+BREAK graceful stop.
	// Следующий старт увидит CleanShutdownFile → PollUntilFree вернётся мгновенно.
	wintun.ResetAdaptiveGap()
	a.mainLogger.Info("Очистка TUN адаптера при выходе...")
	wintun.Shutdown(a.mainLogger)

	if err := a.proxyManager.Disable(); err != nil {
		a.mainLogger.Error("Ошибка при отключении прокси: %v", err)
	}
	if err := trafficstats.SaveToFile(); err != nil {
		a.mainLogger.Warn("Не удалось сохранить счётчик трафика: %v", err)
	}
	a.mainLogger.Info("Работа завершена корректно")
}

// waitForSecretKey ждёт пока secret.key не будет содержать валидный VLESS URL.
// Нужно для wizard-сценария: движок скачивается ~60с, пользователь ещё не вставил ключ.
// При обычном запуске файл уже есть — возвращается мгновенно.
func (a *App) waitForSecretKey() {
	hasKey := func() bool {
		content, err := config.ReadSecretKey(a.cfg.SecretFile)
		if err != nil || strings.TrimSpace(content) == "" {
			return false
		}
		for _, line := range strings.Split(strings.TrimSpace(content), "\n") {
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

func (a *App) extractServerIP(secretFile string) string {
	a.cachedServerIPMu.Lock()
	defer a.cachedServerIPMu.Unlock()

	content, _ := config.ReadSecretKey(secretFile)
	currentHash := sha256.Sum256([]byte(content))
	norm := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return strings.ToLower(r)
		}
		return strings.ToLower(p)
	}
	if a.cachedServerIP != "" && norm(a.cachedForFile) == norm(secretFile) && a.cachedSecretHash == currentHash {
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
	content, err := config.ReadSecretKey(secretFile)
	if err != nil {
		return ""
	}
	// BUG FIX #4: фильтруем комментарии и пустые строки, берём первую валидную.
	// Раньше весь файл обрабатывался как один URL — если в комментарии был '@'
	// (например email), парсинг давал мусорный результат.
	vlessURL := ""
	raw := strings.TrimPrefix(strings.TrimSpace(content), "\xef\xbb\xbf")
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

func setClashMode(ctx context.Context, log logger.Logger, mode string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, config.ClashAPIBase+"/configs",
		strings.NewReader(`{"mode":"`+mode+`"}`))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		if log != nil {
			log.Debug("Clash API недоступен при смене режима на %q: %v", mode, err)
		}
		return
	}
	_ = resp.Body.Close()
	if log != nil {
		log.Info("TUN режим переключён: %s", mode)
	}
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
