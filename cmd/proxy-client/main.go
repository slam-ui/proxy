package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"proxyclient/internal/api"
	"proxyclient/internal/apprules"
	"proxyclient/internal/autorun"
	"proxyclient/internal/config"
	"proxyclient/internal/eventlog"
	"proxyclient/internal/killswitch"
	"proxyclient/internal/netutil"
	"proxyclient/internal/notification"
	"proxyclient/internal/process"
	"proxyclient/internal/proxy"
	"proxyclient/internal/tray"
	"proxyclient/internal/window"
	"proxyclient/internal/wintun"
)

// Кэшируем Win32 прокси на уровне пакета.
// BUG FIX #20: MustLoadDLL паникует в var-блоке ДО любого recover.
// NewLazySystemDLL откладывает загрузку до первого вызова — паника
// перехватывается recover из main() и записывается в crash.log.
var (
	kern32             = syscall.NewLazyDLL("kernel32.dll")
	procProcess32First = kern32.NewProc("Process32FirstW")
	procProcess32Next  = kern32.NewProc("Process32NextW")
	procCreateSnapshot = kern32.NewProc("CreateToolhelp32Snapshot")
	// BUG FIX #NEW-J: переиспользуем глобальный kern32 вместо повторного NewLazyDLL
	// в createSingleInstanceMutex. LoadLibrary вызывается ровно один раз.
	// Имя procCreateMutexW (не procCreateMutex) — чтобы не конфликтовать с
	// одноимённой переменной в orphan_test.go.
	procCreateMutexW = kern32.NewProc("CreateMutexW")
	// БАГ-3: для получения полного пути к процессу (фильтр по директории).
	procQueryFullProcessImageName = kern32.NewProc("QueryFullProcessImageNameW")
)

const (
	logFile         = "safesky.log" // FIX 34: переименован с proxy-client.log
	shutdownTimeout = 10 * time.Second
)

type preflightAddr struct{ name, addr string }

var preflightAddrs = []preflightAddr{
	{"ProxyPort (sing-box inbound)", config.ProxyAddr},
	{"ClashAPI (sing-box API)", config.ClashAPIAddr},
	{"APIAddress (SafeSky)", "localhost" + config.APIAddress},
}

func openLogFile() (*os.File, error) {
	exe, err := os.Executable()
	if err != nil {
		exe = "."
	}
	logPath := filepath.Join(filepath.Dir(exe), logFile)

	// BUG FIX #2: ротация при > 5 MB.
	// O_TRUNC уничтожал лог предыдущего сеанса при перезапуске — краш-диагностика
	// терялась. Теперь дописываем в конец (O_APPEND), а если файл > 5 MB —
	// переименовываем в .old (сохраняем последний сеанс) и начинаем заново.
	const maxLogSize = 5 * 1024 * 1024 // 5 MB
	if fi, statErr := os.Stat(logPath); statErr == nil && fi.Size() > maxLogSize {
		_ = os.Rename(logPath, logPath+".old")
	}

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	// BOM пишем только если файл только что создан (размер 0).
	// В режиме O_APPEND на существующий файл BOM посередине ломает UTF-8.
	if fi, _ := f.Stat(); fi != nil && fi.Size() == 0 {
		_, _ = f.Write([]byte{0xEF, 0xBB, 0xBF}) // UTF-8 BOM для Блокнота
	}
	return f, nil
}

func main() {
	if exe, err := os.Executable(); err == nil {
		_ = os.Chdir(filepath.Dir(exe))
	}

	mutex, err := createSingleInstanceMutex("Global\\ProxyClientSingleInstance")
	if err != nil {
		os.Exit(0)
	}
	defer syscall.CloseHandle(mutex)

	if !isRunningAsAdmin() {
		exePath, _ := os.Executable()
		psCmd := fmt.Sprintf(`Start-Process -FilePath '%s' -Verb RunAs`,
			strings.ReplaceAll(exePath, "'", "''"))
		cmd := exec.Command("powershell", "-WindowStyle", "Hidden", "-Command", psCmd)
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
		if err := cmd.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "[ERROR] Не удалось запустить с правами администратора: %v\n", err)
			os.Exit(1)
		}
		go func() { _ = cmd.Wait() }()
		os.Exit(0)
	}

	runtime.LockOSThread()

	// BUG FIX #1: ранний defer recover удалён.
	// Два defer recover в LIFO-порядке: поздний (с output) перехватывает панику первым,
	// ранний становился недостижимым кодом. Паники до открытия лога теперь обрабатываются
	// единственным defer ниже — он открывает crash.log самостоятельно при output == nil.

	lf, fileErr := openLogFile()
	if fileErr != nil {
		fmt.Fprintf(os.Stderr, "[WARN] Не удалось открыть файл лога: %v\n", fileErr)
	}
	if lf != nil {
		defer func() { _ = lf.Sync(); _ = lf.Close() }()
	}

	stdoutOK := os.Stdout != nil
	if stdoutOK {
		_, errStat := os.Stdout.Stat()
		stdoutOK = errStat == nil
	}
	var output io.Writer
	switch {
	case lf != nil && stdoutOK:
		output = io.MultiWriter(os.Stdout, lf)
	case lf != nil:
		output = lf
	case stdoutOK:
		output = os.Stdout
	default:
		output = io.Discard
	}

	defer func() {
		if r := recover(); r != nil {
			ts := time.Now().Format("2006-01-02 15:04:05")
			msg := fmt.Sprintf("\n[%s] ══ PANIC ══\n[%s] %v\n%s\n", ts, ts, r, debug.Stack())
			if output != nil {
				fmt.Fprint(output, msg)
				if lf != nil {
					_ = lf.Sync()
				}
			} else {
				// Паника случилась ДО открытия лога — пишем напрямую в crash.log.
				if f, ferr := os.OpenFile("crash.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); ferr == nil {
					_, _ = f.WriteString(msg)
					_ = f.Close()
				}
			}
			os.Exit(2)
		}
	}()

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

// preflightCheck проверяет что ни один из критичных портов не занят.
// Запускается до старта sing-box и API-сервера — даёт понятную диагностику
// вместо молчаливого сбоя когда порт занят предыдущим экземпляром или другим VPN.
// При обнаружении занятого порта пытается убить осиротевший sing-box из exeDir
// и повторяет проверку через 2 секунды — без этого при аварийном завершении нужен ручной killall.
func preflightCheck(ctx context.Context, log interface {
	Info(string, ...interface{})
	Error(string, ...interface{})
}) error {
	exeDir := ""
	if exe, err := os.Executable(); err == nil {
		exeDir = strings.ToLower(filepath.Dir(exe))
	}

	for _, a := range preflightAddrs {
		ln, err := net.Listen("tcp", a.addr)
		if err == nil {
			ln.Close()
			continue
		}

		// Порт занят — пробуем убить осиротевший sing-box из нашей директории.
		log.Info("порт %s (%s) занят, пробую освободить...", a.addr, a.name)
		if !killProcessesInDir("sing-box.exe", exeDir, log) {
			// killProcessesInDir ничего не нашёл по exeDir. Это происходит когда
			// os.Executable() вернул путь к тестовому бинарю (go test помещает его
			// во временную директорию), а не к реальному proxy-client.exe.
			// Запасной вариант: убиваем sing-box.exe без фильтрации по директории.
			killProcessesInDir("sing-box.exe", "", log)
		}

		// Ждём 2 секунды чтобы ОС освободила порт (уважаем ctx).
		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			return fmt.Errorf("прерван при ожидании освобождения порта %s", a.addr)
		}

		// Повторная проверка.
		ln2, err2 := net.Listen("tcp", a.addr)
		if err2 != nil {
			log.Error("порт %s (%s) всё ещё занят — завершите предыдущий экземпляр приложения", a.addr, a.name)
			notification.Send("SafeSky — ошибка", fmt.Sprintf("Порт %s занят", a.addr))
			return fmt.Errorf("порт %s (%s) занят — завершите предыдущий экземпляр приложения", a.addr, a.name)
		}
		ln2.Close()
		log.Info("порт %s освобождён успешно", a.addr)
	}
	return nil
}

// initDataDir копирует бандлованные geosite-*.bin из geosite/ в data/
// если они там отсутствуют (первый запуск, ручная установка без build.ps1).
// Файлы НЕ перезаписываются если уже существуют — сохраняем обновлённые версии.
func initDataDir(log interface {
	Info(string, ...interface{})
	Warn(string, ...interface{})
}) {
	if err := os.MkdirAll(config.DataDir, 0755); err != nil {
		log.Warn("initDataDir: не удалось создать %s: %v", config.DataDir, err)
		return
	}
	entries, _ := filepath.Glob("geosite/geosite-*.bin")
	for _, src := range entries {
		dst := filepath.Join(config.DataDir, filepath.Base(src))
		if _, err := os.Stat(dst); err == nil {
			continue // уже существует — не перезаписываем
		}
		data, err := os.ReadFile(src)
		if err != nil {
			log.Warn("initDataDir: не удалось прочитать %s: %v", src, err)
			continue
		}
		if err := os.WriteFile(dst, data, 0644); err != nil {
			log.Warn("initDataDir: %s → %s: %v", filepath.Base(src), dst, err)
		} else {
			log.Info("initDataDir: скопирован %s", filepath.Base(src))
		}
	}
}

// run — главный оркестратор. Создаёт App, поднимает инфраструктуру,
// запускает фоновую инициализацию sing-box, блокируется на трее.
func run(output io.Writer) error {
	flag.Parse()

	cfg := DefaultAppConfig()
	appSettings, settingsErr := config.LoadAppSettings(cfg.SettingsFile)
	if settingsErr == nil {
		cfg.StartProxyOnLaunch = appSettings.StartProxyOnLaunch
	}
	app := NewApp(cfg, output)

	initDataDir(app.mainLogger)
	if settingsErr != nil {
		app.mainLogger.Warn("Не удалось загрузить настройки приложения: %v", settingsErr)
	}

	app.anomaly.Start()
	defer app.anomaly.Stop()

	app.mainLogger.Info("Запуск SafeSky...")
	if autorun.IsEnabled() {
		app.mainLogger.Info("Автозапуск: включён")
	}

	// Предварительная проверка занятости портов — даёт понятную диагностику вместо
	// молчаливого падения sing-box или API-сервера когда порт уже занят.
	if err := preflightCheck(app.lifecycleCtx, app.mainLogger); err != nil {
		return err
	}

	// ОПТИМИЗАЦИЯ: убиваем осиротевший sing-box ПАРАЛЛЕЛЬНО с загрузкой Rules engine.
	// killProcessesByName блокируется на proc.Wait() + time.Sleep(1s) — ~1.2с в логе.
	// Rules engine (I/O: читает app_rules.json) тоже можно запустить сразу.
	// Результат orphan kill нужен ДО старта wintun cleanup — ждём через канал.
	type orphanResult struct{ killed bool }
	orphanCh := make(chan orphanResult, 1)
	go func() {
		killed := killOrphanSingBox(app.mainLogger)
		orphanCh <- orphanResult{killed}
	}()

	// Kill Switch: удаляем правила брандмауэра от предыдущего сеанса.
	// Если приложение упало с активным Kill Switch, правила netsh остаются в системе
	// и блокируют весь трафик до следующего запуска. CleanupOnStart устраняет это.
	killswitch.CleanupOnStart(app.mainLogger)

	// Загружаем app rules параллельно (I/O, не зависит от wintun).
	type appRulesResult struct {
		engine apprules.Engine
		err    error
	}
	appRulesCh := make(chan appRulesResult, 1)
	go func() {
		storage := apprules.NewFileStorage(cfg.AppRulesFile)
		eng, err := apprules.NewPersistentEngine(storage)
		appRulesCh <- appRulesResult{eng, err}
	}()

	// Ждём завершения orphan kill — нужно до wintun cleanup чтобы корректно
	// установить RecordStop/FastDeleteFile перед тем как RemoveStaleTunAdapter начнёт работу.
	orphanRes := <-orphanCh
	if orphanRes.killed {
		wintun.RecordStop()
		// После kill orphan пробуем синхронно освободить wintun kernel-объект.
		// proc.Wait() уже вернулся — sing-box выгрузил wintun.dll handle.
		// Если ForceDeleteAdapter успешен — PollUntilFree пропустит полный gap (60с → ~7-10с).
		if wintun.ForceDeleteAdapter(app.lifecycleCtx, config.TunInterfaceName) {
			app.mainLogger.Info("wintun: orphan kill → ForceDeleteAdapter succeeded — gap будет пропущен")
			_ = os.WriteFile(wintun.FastDeleteFile, []byte("1"), 0644)
		}
	}

	xrayCfg := app.buildXRayCfg()
	// Override: <local> — все хосты без точки (включая "localhost") обходят прокси.
	// Явно добавляем 127.0.0.1 и ::1 — на Windows <local> не всегда покрывает IP-формат.
	// Это исправляет OAuth callback для Claude Code, Codex и других CLI-инструментов:
	// они стартуют локальный HTTP-сервер на случайном порту, браузер (с системным прокси)
	// должен добраться до него без прокси — иначе редирект http://localhost:PORT/callback
	// идёт через sing-box и может падать при перезапуске TUN.
	proxyConfig := proxy.Config{Address: config.ProxyAddr, Override: api.DefaultProxyOverride}

	// API сервер создаётся БЕЗ xrayManager (nil) — он будет установлен фоновой горутиной.
	// /api/status возвращает warming=true пока менеджер не установлен.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	app.quit = make(chan struct{})
	app.apiServer = api.NewServer(api.Config{
		ListenAddress: cfg.APIAddress,
		XRayManager:   nil,
		ProxyManager:  app.proxyManager,
		ConfigPath:    cfg.RuntimeFile,
		SecretKeyPath: cfg.SecretFile,
		Logger:        app.mainLogger,
		EventLog:      app.evLog,
		QuitChan:      app.quit,
		// Мгновенно обновляем список серверов в трее при смене сервера через UI.
		SecretKeyUpdatedFn: func() {
			go app.refreshTrayServers(cfg.APIAddress)
		},
	}, app.lifecycleCtx)

	// Собираем app rules.
	appRulesRes := <-appRulesCh
	rulesEngine := appRulesRes.engine
	if appRulesRes.err != nil {
		app.mainLogger.Warn("Failed to initialize rules engine: %v", appRulesRes.err)
		rulesEngine = nil
	} else {
		app.mainLogger.Info("Rules engine инициализирован (%d правил)", len(rulesEngine.ListRules()))
	}

	var processMonitor process.Monitor
	var processLauncher process.Launcher
	monitorLogger := eventlog.NewLogger(app.appLogger, app.evLog, "monitor")
	if rulesEngine != nil {
		// OPT #2: передаём engine в монитор — refresh() будет кэшировать ProxyStatus
		// прямо во время сканирования процессов вместо пересчёта на каждый HTTP-запрос.
		processMonitor = process.NewMonitorWithEngine(monitorLogger, rulesEngine)
		if err := processMonitor.Start(); err != nil {
			app.mainLogger.Warn("Failed to start process monitor: %v", err)
		} else {
			app.mainLogger.Info("Process monitor запущен")
		}
		processLauncher = process.NewLauncher(monitorLogger, rulesEngine)
	}

	app.apiServer.SetupTunRoutes(xrayCfg)
	app.apiServer.SetupFeatureRoutes(ctx)
	if rulesEngine != nil && processMonitor != nil && processLauncher != nil {
		app.apiServer.SetupAppProxyRoutes(rulesEngine, processMonitor, processLauncher)
	}
	app.apiServer.FinalizeRoutes()

	apiReady := make(chan struct{})
	go func() {
		if startErr := app.apiServer.Start(ctx); startErr != nil {
			app.mainLogger.Error("Ошибка API сервера: %v", startErr)
		}
	}()
	go func() {
		if waitErr := netutil.WaitForPort(ctx, "localhost"+cfg.APIAddress, 5*time.Second); waitErr == nil {
			app.mainLogger.Info("API сервер готов на %s", cfg.APIAddress)
		} else {
			app.mainLogger.Warn("API сервер не ответил за 5с: %v", waitErr)
		}
		close(apiReady)
	}()

	app.mainLogger.Info("Инициализация завершена")

	// Фоновая горутина: wintun → sing-box → proxy enable.
	app.startBackground(xrayCfg, proxyConfig, apiReady)

	// Открываем окно когда трей и API готовы.
	go func() {
		tray.WaitReady()
		app.mainLogger.Info("Трей готов, ожидаем API сервер...")
		<-apiReady
		app.mainLogger.Info("Открываем панель управления: %s", cfg.WebUIURL)
		window.Open(cfg.WebUIURL)
	}()

	go func() {
		tray.WaitReady()
		<-apiReady
		app.refreshTrayServers(cfg.APIAddress)
		// Периодически синхронизируем список серверов в трее (каждые 15с).
		// Это гарантирует актуальность списка если пользователь добавил/переключил
		// сервер через UI, а не через меню трея.
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-app.quit:
				return
			case <-t.C:
				app.refreshTrayServers(cfg.APIAddress)
			}
		}
	}()

	tray.SetProxyAddr(proxyConfig.Address)
	// Регистрируем callback для переноса окна на передний план (правый клик по трею).
	tray.SetBringToFront(func() { window.BringToFront(cfg.WebUIURL) })

	// BUG FIX #NEW-3: OnQuit callback трея не был защищён sync.Once.
	// handleQuit (POST /api/quit) уже использует s.quitOnce.Do(...).
	// Если systray вызвал бы OnQuit дважды (двойной клик, некоторые версии Windows)
	// → close(уже закрытого канала) → panic: close of closed channel.
	var trayQuitOnce sync.Once
	tray.Run(tray.Callbacks{
		OnOpen: func() { window.BringToFront(cfg.WebUIURL) },
		OnCopyAddr: func(addr string) {
			if addr == "" {
				return
			}
			cmd := exec.Command("powershell", "-WindowStyle", "Hidden", "-NonInteractive", "-Command",
				"Set-Clipboard -Value '"+strings.ReplaceAll(addr, "'", "''")+"'")
			cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
			_ = cmd.Run()
		},
		OnEnable: func() {
			if app.apiServer.IsWarming() {
				notification.Send("SafeSky", "Инициализация... подождите")
				return
			}
			if err := app.proxyManager.Enable(proxyConfig); err != nil {
				app.mainLogger.Error("Ошибка включения прокси: %v", err)
			} else {
				tray.SetEnabled(true)
				app.mainLogger.Info("Прокси включён через трей")
				notification.Send("SafeSky", "Прокси включён ✓")
			}
		},
		OnDisable: func() {
			if err := app.proxyManager.Disable(); err != nil {
				app.mainLogger.Error("Ошибка отключения прокси: %v", err)
			} else {
				tray.SetEnabled(false)
				app.mainLogger.Info("Прокси отключён через трей")
				notification.Send("SafeSky", "Прокси отключён")
			}
		},
		OnServerSwitch: func(serverID string) {
			if serverID == "" {
				return
			}
			if app.apiServer.IsWarming() {
				notification.Send("SafeSky", "Инициализация... подождите")
				return
			}
			go func() {
				if err := app.connectTrayServer(cfg.APIAddress, serverID); err != nil {
					app.mainLogger.Error("Не удалось переключить сервер: %v", err)
					notification.Send("SafeSky", "Не удалось переключить сервер")
					return
				}
				app.refreshTrayServers(cfg.APIAddress)
			}()
		},
		OnQuit: func() { trayQuitOnce.Do(func() { close(app.quit) }) },
	})

	<-app.quit

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()
	app.Shutdown(shutdownCtx, processMonitor)
	return nil
}

// refreshTrayServers обновляет список серверов в меню трея через локальный API.
func (a *App) refreshTrayServers(apiAddress string) {
	type serverListResponse struct {
		Servers []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"servers"`
		ActiveID string `json:"active_id"`
	}

	url := "http://localhost" + apiAddress + "/api/servers"
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		a.mainLogger.Warn("Не удалось получить список серверов из API: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		a.mainLogger.Warn("API /api/servers вернул статус %d", resp.StatusCode)
		return
	}

	var listResp serverListResponse
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		a.mainLogger.Warn("Не удалось распарсить список серверов: %v", err)
		return
	}

	items := make([]tray.ServerItem, 0, len(listResp.Servers))
	activeName := ""
	for _, srv := range listResp.Servers {
		isActive := srv.ID == listResp.ActiveID
		// FIX: используем реальное имя из VLESS URL-фрагмента (#Название).
		// Аналог JS serverDisplayName() в index.html.
		// Большинство VLESS URL имеют вид: vless://uuid@host:port?params#Имя Сервера
		displayName := serverDisplayName(srv.URL, srv.Name)
		items = append(items, tray.ServerItem{
			ID:     srv.ID,
			Name:   displayName,
			Active: isActive,
		})
		if isActive {
			activeName = displayName
		}
	}
	if len(items) == 0 {
		items = append(items, tray.ServerItem{Name: "Нет серверов", Active: false})
	}

	tray.SetServerList(items)
	tray.SetActiveServer(activeName)
}

// serverDisplayName возвращает отображаемое имя сервера.
// Приоритет: 1) фрагмент VLESS URL (#Название), 2) сохранённое имя в servers.json.
// Логика совпадает с JS функцией serverDisplayName() в index.html.
func serverDisplayName(vlessURL, fallbackName string) string {
	if vlessURL != "" {
		// Ищем фрагмент после #
		if i := strings.LastIndex(vlessURL, "#"); i >= 0 && i+1 < len(vlessURL) {
			fragment := strings.TrimSpace(vlessURL[i+1:])
			if fragment != "" {
				// URL-decode фрагмента (простой decode без url.PathUnescape который требует импорт)
				decoded := urlDecodeFragment(fragment)
				if decoded != "" {
					return decoded
				}
			}
		}
	}
	if fallbackName != "" {
		return fallbackName
	}
	return "Сервер"
}

// urlDecodeFragment декодирует URL-encoded строку (%XX → символ).
func urlDecodeFragment(s string) string {
	result := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		if s[i] == '%' && i+2 < len(s) {
			hi := hexVal(s[i+1])
			lo := hexVal(s[i+2])
			if hi >= 0 && lo >= 0 {
				result = append(result, byte(hi<<4|lo))
				i += 3
				continue
			}
		}
		if s[i] == '+' {
			result = append(result, ' ')
			i++
			continue
		}
		result = append(result, s[i])
		i++
	}
	return string(result)
}

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

func (a *App) connectTrayServer(apiAddress, serverID string) error {
	url := fmt.Sprintf("http://localhost%s/api/servers/%s/connect", apiAddress, serverID)
	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("API returned %d", resp.StatusCode)
	}
	return nil
}

// ── Windows helpers ───────────────────────────────────────────────────────────

func killOrphanSingBox(log interface{ Info(string, ...interface{}) }) bool {
	// БАГ-3: убиваем только sing-box.exe из нашей директории.
	// Clash, Hiddify и другие VPN-клиенты тоже используют sing-box.exe —
	// убивать их нельзя.
	exeDir := ""
	if exe, err := os.Executable(); err == nil {
		exeDir = strings.ToLower(filepath.Dir(exe))
	}
	return killProcessesInDir("sing-box.exe", exeDir, log)
}

// killProcessesInDir — как killProcessesByName, но убивает только процессы из exeDir.
// Защита: не трогаем sing-box.exe принадлежащий другим VPN-приложениям.
func killProcessesInDir(targetName string, exeDir string, log interface{ Info(string, ...interface{}) }) bool {
	snap, err := createToolhelp32Snapshot()
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(snap)

	type entry32 struct {
		Size              uint32
		CntUsage          uint32
		ProcessID         uint32
		DefaultHeapID     uintptr
		ModuleID          uint32
		CntThreads        uint32
		ParentProcessID   uint32
		PriorityClassBase int32
		Flags             uint32
		ExeFile           [syscall.MAX_PATH]uint16
	}

	var e entry32
	e.Size = uint32(unsafe.Sizeof(e))
	selfPID := uint32(os.Getpid())
	var procs []*os.Process
	ret, _, _ := procProcess32First.Call(uintptr(snap), uintptr(unsafe.Pointer(&e)))
	for ret != 0 {
		name := syscall.UTF16ToString(e.ExeFile[:])
		if strings.EqualFold(name, targetName) && e.ProcessID != selfPID {
			// Проверяем директорию процесса если exeDir задан
			if exeDir != "" {
				procPath := getProcessExePath(e.ProcessID)
				if procPath != "" && !strings.EqualFold(filepath.Dir(procPath), exeDir) {
					ret, _, _ = procProcess32Next.Call(uintptr(snap), uintptr(unsafe.Pointer(&e)))
					continue
				}
			}
			if proc, err := os.FindProcess(int(e.ProcessID)); err == nil {
				log.Info("Завершаем осиротевший процесс %s (PID: %d)", targetName, e.ProcessID)
				_ = proc.Kill()
				procs = append(procs, proc)
			}
		}
		ret, _, _ = procProcess32Next.Call(uintptr(snap), uintptr(unsafe.Pointer(&e)))
	}
	if len(procs) == 0 {
		return false
	}
	for _, p := range procs {
		done := make(chan struct{}, 1)
		go func(proc *os.Process) {
			_, _ = proc.Wait()
			close(done)
		}(p)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			log.Info("Таймаут ожидания завершения sing-box.exe (PID: %d) — продолжаем", p.Pid)
		}
	}
	time.Sleep(1 * time.Second)
	return true
}

func killProcessesByName(targetName string, log interface{ Info(string, ...interface{}) }) bool {
	snap, err := createToolhelp32Snapshot()
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(snap)

	type entry32 struct {
		Size              uint32
		CntUsage          uint32
		ProcessID         uint32
		DefaultHeapID     uintptr
		ModuleID          uint32
		CntThreads        uint32
		ParentProcessID   uint32
		PriorityClassBase int32
		Flags             uint32
		ExeFile           [syscall.MAX_PATH]uint16
	}

	var e entry32
	e.Size = uint32(unsafe.Sizeof(e))
	selfPID := uint32(os.Getpid())
	var procs []*os.Process
	ret, _, _ := procProcess32First.Call(uintptr(snap), uintptr(unsafe.Pointer(&e)))
	for ret != 0 {
		name := syscall.UTF16ToString(e.ExeFile[:])
		if strings.EqualFold(name, targetName) && e.ProcessID != selfPID {
			if proc, err := os.FindProcess(int(e.ProcessID)); err == nil {
				log.Info("Завершаем осиротевший процесс %s (PID: %d)", targetName, e.ProcessID)
				_ = proc.Kill()
				procs = append(procs, proc)
			}
		}
		ret, _, _ = procProcess32Next.Call(uintptr(snap), uintptr(unsafe.Pointer(&e)))
	}
	if len(procs) == 0 {
		return false
	}
	// Ждём завершения всех убитых процессов — без этого kernel WinTun объект
	// остаётся живым и sing-box падает с FATAL[0015] через 15с после старта.
	// BUG FIX: proc.Wait() без таймаута может заблокировать main() навсегда
	// (zombie-процесс или kernel lock). Используем горутину + select с таймаутом 5с.
	for _, p := range procs {
		done := make(chan struct{}, 1)
		go func(proc *os.Process) {
			_, _ = proc.Wait()
			close(done)
		}(p)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			log.Info("Таймаут ожидания завершения sing-box.exe (PID: %d) — продолжаем", p.Pid)
		}
	}
	// Дополнительная пауза для освобождения kernel handles после Wait
	time.Sleep(1 * time.Second)
	return true
}

func createToolhelp32Snapshot() (syscall.Handle, error) {
	const TH32CS_SNAPPROCESS = 0x00000002
	h, _, err := procCreateSnapshot.Call(TH32CS_SNAPPROCESS, 0)
	if h == uintptr(syscall.InvalidHandle) {
		return syscall.InvalidHandle, err
	}
	return syscall.Handle(h), nil
}

// getProcessExePath возвращает полный путь к исполняемому файлу процесса.
// Использует QueryFullProcessImageNameW (Vista+). Возвращает "" при ошибке.
func getProcessExePath(pid uint32) string {
	const PROCESS_QUERY_LIMITED_INFORMATION = 0x1000
	hProc, err := syscall.OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer syscall.CloseHandle(hProc)

	buf := make([]uint16, syscall.MAX_PATH)
	size := uint32(len(buf))
	ret, _, _ := procQueryFullProcessImageName.Call(
		uintptr(hProc), 0,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
	)
	if ret == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:size])
}

// isRunningAsAdmin проверяет привилегии через Windows token elevation.
// BUG FIX #5: проверка через PHYSICALDRIVE0 ненадёжна — на системах с BitLocker,
// шифрованием диска или в виртуальных машинах диск может быть недоступен даже
// с правами администратора. windows.GetCurrentProcessToken().IsElevated() —
// официальный способ проверки из golang.org/x/sys/windows.
func isRunningAsAdmin() bool {
	// BUG FIX #5: IsElevated() возвращает только bool, без error.
	return windows.GetCurrentProcessToken().IsElevated()
}

func createSingleInstanceMutex(name string) (syscall.Handle, error) {
	// BUG FIX #NEW-J: используем глобальный procCreateMutexW вместо повторного NewLazyDLL.
	namePtr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	h, _, lastErr := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(namePtr)))
	if h == 0 {
		return 0, lastErr
	}
	const ERROR_ALREADY_EXISTS = 183
	if lastErr == syscall.Errno(ERROR_ALREADY_EXISTS) {
		_ = syscall.CloseHandle(syscall.Handle(h))
		return 0, fmt.Errorf("already running")
	}
	return syscall.Handle(h), nil
}
