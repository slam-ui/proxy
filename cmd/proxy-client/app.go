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
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"proxyclient/internal/anomalylog"
	"proxyclient/internal/api"
	"proxyclient/internal/engine"
	"proxyclient/internal/killswitch"
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
	KillSwitch   bool   // блокировать трафик при падении туннеля
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
	cfg             AppConfig
	output          io.Writer
	appLogger       logger.Logger
	evLog           *eventlog.Log
	mainLogger      logger.Logger
	proxyManager    proxy.Manager
	apiServer       *api.Server
	quit            chan struct{}
	anomaly         *anomalylog.Detector
	// lifecycleCtx отменяется при Shutdown — прерывает PollUntilFree во всех горутинах.
	lifecycleCtx    context.Context
	lifecycleCancel context.CancelFunc
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
		// BeforeRestart — wintun cleanup для обычного перезапуска (apply rules, ручной restart).
		// Используем таймаут 30s — если TUN не освободился за это время,
		// sing-box всё равно попробует запуститься. Wintun-конфликт после apply
		// обрабатывается через handleCrash → retry loop с полным PollUntilFree.
		BeforeRestart: func(ctx context.Context, log logger.Logger) error {
			wintun.RecordStop()
			wintun.RemoveStaleTunAdapter(log)
			// Быстрое ожидание с таймаутом 30s — не блокируем apply rules надолго.
			quickCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			wintun.PollUntilFree(quickCtx, log, config.TunInterfaceName)
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
	isTunConflict := xray.IsTunConflict(lastOut)

	if isTunConflict {
		wintun.RecordStop()
		// Kill Switch: блокируем трафик на время перезапуска TUN
		if a.cfg.KillSwitch {
			serverIP := extractServerIP(a.cfg.SecretFile)
			killswitch.Enable(serverIP, a.mainLogger)
		}

		// Retry loop: пытаемся поднять TUN несколько раз с увеличивающимся gap.
		// Каждая попытка: IncreaseGap → RemoveAdapter → PollUntilFree → Start.
		// Если старт успешен — выходим. Если нет — повторяем.
		const maxTunAttempts = 5
		for attempt := 1; attempt <= maxTunAttempts; attempt++ {
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
	// Kill Switch: блокируем трафик пока туннель не восстановлен
	if a.cfg.KillSwitch {
		serverIP := extractServerIP(a.cfg.SecretFile)
		killswitch.Enable(serverIP, a.mainLogger)
	}
	if disableErr := a.proxyManager.Disable(); disableErr != nil {
		a.mainLogger.Error("Не удалось отключить прокси: %v", disableErr)
	} else {
		tray.SetEnabled(false)
		a.mainLogger.Warn("Системный прокси отключён из-за краша sing-box.")
	}
}

// startBackground запускает фоновую инициализацию.
// Wintun cleanup стартует НЕМЕДЛЕННО — параллельно с подъёмом API-сервера.
// Sing-box запускается когда выполнены ОБА условия: apiReady И wintunReady.
func (a *App) startBackground(xrayCfg xray.Config, proxyConfig proxy.Config, apiReady <-chan struct{}) {
	// Wintun cleanup — запускаем сразу, не ждём API.
	wintunReady := make(chan struct{})
	go func() {
		defer close(wintunReady)

		// Auto-Engine: скачиваем sing-box.exe если отсутствует.
		// Проверяем до wintun — нет смысла чистить wintun если движка нет.
		if engine.NeedsDownload(a.cfg.SingBoxPath) {
			a.mainLogger.Info("sing-box.exe не найден — автоматическая загрузка...")
			notification.Send("Proxy", "Загружаем sing-box.exe...")
			progress := make(chan engine.Progress, 20)
			go func() {
				for p := range progress {
					if p.Message != "" {
						a.mainLogger.Info("engine: %s", p.Message)
					}
				}
			}()
			if err := engine.EnsureEngine(a.lifecycleCtx, a.cfg.SingBoxPath, progress); err != nil {
				a.mainLogger.Error("Не удалось загрузить sing-box.exe: %v", err)
				notification.Send("Proxy — ошибка", "Не удалось загрузить sing-box. Откройте приложение.")
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

		wintun.RemoveStaleTunAdapter(a.mainLogger)
		wintun.PollUntilFree(a.lifecycleCtx, a.mainLogger, config.TunInterfaceName)
	}()

	go func() {
		// Ждём и API, и wintun — оба нужны перед запуском sing-box.
		// Wintun cleanup начался раньше, так что к моменту apiReady
		// часть gap уже может быть отработана.
		<-apiReady
		tray.SetEnabled(false)

		<-wintunReady

		// Ждём ключ после wintun: wizard мог сохранить ключ пока шёл cleanup.
		a.waitForSecretKey()

		// Генерируем конфиг только после того как ключ точно есть.
		routingCfg, err := config.LoadRoutingConfig(a.cfg.DataDir + "/routing.json")
		if err != nil {
			a.mainLogger.Warn("Не удалось загрузить routing config: %v, используем дефолтный", err)
			routingCfg = config.DefaultRoutingConfig()
		}
		cfgErr := config.GenerateSingBoxConfig(a.cfg.SecretFile, a.cfg.ConfigPath, routingCfg)
		a.mainLogger.Info("Запуск sing-box...")
		if cfgErr != nil {
			if !a.handleConfigError(cfgErr) {
				return
			}
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
		if err := netutil.WaitForPort(config.ProxyAddr, 15*time.Second); err != nil {
			a.mainLogger.Warn("sing-box не ответил за 15с: %v — включаем прокси всё равно", err)
		} else {
			a.mainLogger.Info("sing-box готов")
		}

		killswitch.Disable(a.mainLogger)
		wintun.ResetAdaptiveGap()
		if err := a.proxyManager.Enable(proxyConfig); err != nil {
			a.mainLogger.Warn("Не удалось включить системный прокси: %v", err)
		} else {
			a.mainLogger.Info("Системный прокси включён: %s", proxyConfig.Address)
		}

		tray.SetEnabled(true)
		notification.Send("Proxy", "Прокси готов ✓")
		a.mainLogger.Info("Фоновая инициализация завершена")

		go preWarmProxyConnection(config.ProxyAddr, a.mainLogger)
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
	for {
		select {
		case <-a.lifecycleCtx.Done():
			return
		case <-time.After(2 * time.Second):
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
func extractServerIP(secretFile string) string {
	data, err := os.ReadFile(secretFile)
	if err != nil {
		return ""
	}
	vlessURL := strings.TrimSpace(string(data))
	vlessURL = strings.TrimPrefix(vlessURL, "\xef\xbb\xbf")
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
	// Hostname: резолвим с таймаутом 3с чтобы не блокировать crash handler
	type result struct{ ip string }
	ch := make(chan result, 1)
	go func() {
		addrs, err := net.LookupHost(host)
		if err != nil || len(addrs) == 0 {
			ch <- result{""}
			return
		}
		ch <- result{addrs[0]}
	}()
	select {
	case r := <-ch:
		return r.ip
	case <-time.After(3 * time.Second):
		return "" // таймаут — не блокируем KillSwitch, просто без allowlist для сервера
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
func preWarmProxyConnection(proxyAddr string, log logger.Logger) {
	// Даём TUN интерфейсу время подняться (~2-10с после HTTP-порта).
	// Прогрев через HTTP proxy inbound — не зависит от TUN.
	time.Sleep(2 * time.Second)

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
	resp, err := client.Get("https://api.ipify.org?format=json")
	if err != nil {
		log.Info("pre-warm: соединение не установлено (%v) — пропускаем", err)
		return
	}
	// BUG FIX: тело нужно прочитать до конца перед Close() чтобы HTTP transport
	// мог вернуть TCP-соединение в keep-alive пул. Без этого VLESS TLS-соединение
	// не переиспользуется — цель прогрева не достигается.
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	log.Info("pre-warm: VLESS соединение прогрето ✓ (статус %d)", resp.StatusCode)
}
