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
	"syscall"
	"time"
	"unsafe"

	"proxyclient/internal/api"
	"proxyclient/internal/apprules"
	"proxyclient/internal/autorun"
	"proxyclient/internal/config"
	"proxyclient/internal/eventlog"
	"proxyclient/internal/netutil"
	"proxyclient/internal/notification"
	"proxyclient/internal/process"
	"proxyclient/internal/proxy"
	"proxyclient/internal/tray"
	"proxyclient/internal/window"
	"proxyclient/internal/wintun"
)

// Кэшируем Win32 прокси на уровне пакета.
var (
	kern32             = syscall.MustLoadDLL("kernel32.dll")
	procProcess32First = kern32.MustFindProc("Process32FirstW")
	procProcess32Next  = kern32.MustFindProc("Process32NextW")
	procCreateSnapshot = kern32.MustFindProc("CreateToolhelp32Snapshot")
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

	// Ранняя защита от паники — до открытия лога.
	defer func() {
		if r := recover(); r != nil {
			msg := fmt.Sprintf("[%s] EARLY PANIC: %v\n%s\n",
				time.Now().Format("2006-01-02 15:04:05"), r, debug.Stack())
			if f, ferr := os.OpenFile("crash.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); ferr == nil {
				_, _ = f.WriteString(msg)
				_ = f.Close()
			}
			panic(r)
		}
	}()

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
			fmt.Fprintf(output, "\n[%s] ══ PANIC ══\n[%s] %v\n%s\n", ts, ts, r, debug.Stack())
			if lf != nil {
				_ = lf.Sync()
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

	// Убиваем осиротевший sing-box и сразу фиксируем время остановки.
	// RecordStop() вызывается ВСЕГДА — гарантирует минимальный gap при следующем старте
	// даже если приложение упало без graceful shutdown.
	killOrphanSingBox(app.mainLogger)
	wintun.RecordStop()

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
		Logger:        app.mainLogger,
		EventLog:      app.evLog,
		QuitChan:      app.quit,
	})

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
		processMonitor = process.NewMonitor(monitorLogger)
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
		if waitErr := netutil.WaitForPort("localhost"+cfg.APIAddress, 5*time.Second); waitErr == nil {
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
		OnQuit: func() { close(app.quit) },
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
	killed := false
	ret, _, _ := procProcess32First.Call(uintptr(snap), uintptr(unsafe.Pointer(&e)))
	for ret != 0 {
		name := syscall.UTF16ToString(e.ExeFile[:])
		if strings.EqualFold(name, targetName) && e.ProcessID != selfPID {
			if proc, err := os.FindProcess(int(e.ProcessID)); err == nil {
				log.Info("Завершаем осиротевший процесс %s (PID: %d)", targetName, e.ProcessID)
				_ = proc.Kill()
				killed = true
			}
		}
		ret, _, _ = procProcess32Next.Call(uintptr(snap), uintptr(unsafe.Pointer(&e)))
	}
	return killed
}

func createToolhelp32Snapshot() (syscall.Handle, error) {
	const TH32CS_SNAPPROCESS = 0x00000002
	h, _, err := procCreateSnapshot.Call(TH32CS_SNAPPROCESS, 0)
	if h == uintptr(syscall.InvalidHandle) {
		return syscall.InvalidHandle, err
	}
	return syscall.Handle(h), nil
}

func isRunningAsAdmin() bool {
	f, err := os.Open("\\\\.\\PHYSICALDRIVE0")
	if err == nil {
		_ = f.Close()
		return true
	}
	return false
}

func createSingleInstanceMutex(name string) (syscall.Handle, error) {
	k32 := syscall.NewLazyDLL("kernel32.dll")
	createMutex := k32.NewProc("CreateMutexW")
	namePtr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	h, _, lastErr := createMutex.Call(0, 0, uintptr(unsafe.Pointer(namePtr)))
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
