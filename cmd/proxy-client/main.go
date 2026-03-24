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
)
const (
	logFile         = "proxy-client.log"
	shutdownTimeout = 10 * time.Second
)

func openLogFile() (*os.File, error) {
	exe, err := os.Executable()
	if err != nil {
		exe = "."
	}
	f, err := os.OpenFile(filepath.Join(filepath.Dir(exe), logFile),
		os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	_, _ = f.Write([]byte{0xEF, 0xBB, 0xBF}) // UTF-8 BOM для Блокнота
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
		_ = cmd.Start()
		os.Exit(0)
	}

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

// run — главный оркестратор. Создаёт App, поднимает инфраструктуру,
// запускает фоновую инициализацию sing-box, блокируется на трее.
func run(output io.Writer) error {
	cfg := DefaultAppConfig()
	app := NewApp(cfg, output)

	app.anomaly.Start()
	defer app.anomaly.Stop()

	app.mainLogger.Info("Запуск прокси-клиента...")
	if autorun.IsEnabled() {
		app.mainLogger.Info("Автозапуск: включён")
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
		if wintun.ForceDeleteAdapter(config.TunInterfaceName) {
			app.mainLogger.Info("wintun: orphan kill → ForceDeleteAdapter succeeded — gap будет пропущен")
			_ = os.WriteFile(wintun.FastDeleteFile, []byte("1"), 0644)
		}
	}

	xrayCfg := app.buildXRayCfg()
	proxyConfig := proxy.Config{Address: config.ProxyAddr, Override: "<local>"}

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

	tray.SetProxyAddr(proxyConfig.Address)
	// BUG FIX #NEW-3: OnQuit callback трея не был защищён sync.Once.
	// handleQuit (POST /api/quit) уже использует s.quitOnce.Do(...).
	// Если systray вызвал бы OnQuit дважды (двойной клик, некоторые версии Windows)
	// → close(уже закрытого канала) → panic: close of closed channel.
	var trayQuitOnce sync.Once
	tray.Run(tray.Callbacks{
		OnOpen: func() { window.Open(cfg.WebUIURL) },
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
				notification.Send("Proxy", "Инициализация... подождите")
				return
			}
			if err := app.proxyManager.Enable(proxyConfig); err != nil {
				app.mainLogger.Error("Ошибка включения прокси: %v", err)
			} else {
				tray.SetEnabled(true)
				app.mainLogger.Info("Прокси включён через трей")
				notification.Send("Proxy", "Прокси включён ✓")
			}
		},
		OnDisable: func() {
			if err := app.proxyManager.Disable(); err != nil {
				app.mainLogger.Error("Ошибка отключения прокси: %v", err)
			} else {
				tray.SetEnabled(false)
				app.mainLogger.Info("Прокси отключён через трей")
				notification.Send("Proxy", "Прокси отключён")
			}
		},
		OnQuit: func() { trayQuitOnce.Do(func() { close(app.quit) }) },
	})

	<-app.quit

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()
	app.Shutdown(shutdownCtx, processMonitor)
	return nil
}

// ── Windows helpers ───────────────────────────────────────────────────────────

func killOrphanSingBox(log interface{ Info(string, ...interface{}) }) bool {
	return killProcessesByName("sing-box.exe", log)
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
