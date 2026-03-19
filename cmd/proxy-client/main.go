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

	"proxyclient/internal/anomalylog"
	"proxyclient/internal/api"
	"proxyclient/internal/apprules"
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

const (
	secretFile       = "secret.key"
	runtimeFile      = "config.runtime.json"
	logFile          = "proxy-client.log"
	apiListenAddress = ":8080"
)

// dataDir — папка с данными приложения (geosite .bin, routing.json, app_rules.json).
// Должна совпадать с config.DataDir.
const (
	dataDir         = config.DataDir
	appRulesFile    = dataDir + "/app_rules.json"
	webUIURL        = "http://localhost:8080"
	shutdownTimeout = 10 * time.Second
)

// openLogFile открывает файл лога в режиме append|create рядом с exe.
// Логи сохраняются даже если UI не запустился — для анализа краша.
func openLogFile() (*os.File, error) {
	exe, err := os.Executable()
	if err != nil {
		exe = "."
	}
	logPath := filepath.Join(filepath.Dir(exe), logFile)
	// O_TRUNC: каждый запуск начинает чистый лог — только текущая сессия.
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	// UTF-8 BOM — без него Windows Блокнот открывает файл как ANSI → кракозябры.
	// Записываем в начало файла: Go записывает UTF-8, BOM говорит Notepad об этом.
	_, _ = f.Write([]byte{0xEF, 0xBB, 0xBF})
	return f, nil
}

func main() {
	// ── Переходим в папку с .exe ─────────────────────────────────────────────
	// Все пути в проекте относительные: "./sing-box.exe", "routing.json" и т.д.
	// Они резолвятся относительно РАБОЧЕЙ ДИРЕКТОРИИ, а не папки с .exe.
	// При запуске двойным кликом с рабочего стола cwd = Desktop → файлы не найдены
	// → приложение падает мгновенно, лог пуст.
	if exe, err := os.Executable(); err == nil {
		_ = os.Chdir(filepath.Dir(exe))
	}

	// Единственный экземпляр — именованный мьютекс Windows.
	// Предотвращает двойной запуск (конфликт портов 8080 и 10807).
	mutex, err := createSingleInstanceMutex("Global\\ProxyClientSingleInstance")
	if err != nil {
		os.Exit(0) // уже запущен — тихо выходим
	}
	defer syscall.CloseHandle(mutex)

	// TUN-интерфейс требует прав администратора.
	// Если запущены без прав — перезапускаемся через UAC.
	if !isRunningAsAdmin() {
		exePath, _ := os.Executable()
		// BUG FIX: Start-Process — это cmdlet PowerShell, а не аргументы exe.
		// Старый вариант передавал "-FilePath" как отдельный аргумент powershell.exe,
		// что вызывало "The term 'Start-Process' is not recognized" при путях с пробелами.
		// Теперь весь вызов передаётся как единая строка -Command.
		psCmd := fmt.Sprintf(`Start-Process -FilePath '%s' -Verb RunAs`,
			strings.ReplaceAll(exePath, "'", "''")) // экранируем одиночные кавычки
		cmd := exec.Command("powershell", "-WindowStyle", "Hidden", "-Command", psCmd)
		_ = cmd.Start()
		os.Exit(0)
	}

	// ── Ранняя защита от паники — до открытия нормального лога ───────────────
	// Если что-то падает между Chdir и первой записью в лог, эта горутина
	// перехватывает панику и пишет её в отдельный файл crash.log.
	defer func() {
		if r := recover(); r != nil {
			msg := fmt.Sprintf("[%s] EARLY PANIC: %v\n%s\n",
				time.Now().Format("2006-01-02 15:04:05"), r, debug.Stack())
			if f, err := os.OpenFile("crash.log",
				os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
				_, _ = f.WriteString(msg)
				_ = f.Close()
			}
			panic(r) // re-panic чтобы нормальный recover тоже сработал
		}
	}()

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

	// Весь вывод — в файл, и в консоль если она доступна.
	// В режиме -H windowsgui дескриптор stdout невалиден: запись в os.Stdout
	// вызывает panic ещё до первой строки лога — файл остаётся пустым.
	// Проверяем доступность stdout через Stat() перед тем как включать его в вывод.
	// В режиме -H windowsgui os.Stdout невалиден: запись в него — panic.
	// Проверяем через Stat(): если возвращает ошибку — stdout недоступен.
	stdoutOK := false
	if os.Stdout != nil {
		_, errStat := os.Stdout.Stat() // (FileInfo, error) — берём error
		stdoutOK = errStat == nil
	}
	var output io.Writer
	switch {
	case lf != nil && stdoutOK:
		output = io.MultiWriter(os.Stdout, lf)
	case lf != nil:
		output = lf // windowsgui: только в файл
	case stdoutOK:
		output = os.Stdout
	default:
		output = io.Discard
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

	// Детектор аномалий: следит за event log и при ошибках/крашах
	// сохраняет диагностический файл anomaly-YYYY-MM-DD_HH-MM-SS_<kind>.log
	// рядом с .exe — чтобы можно было разобрать что случилось без открытия приложения.
	exeDir := "."
	if exe, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(exe)
	}
	anomalyDetector := anomalylog.New(evLog, exeDir)
	anomalyDetector.Start()
	defer anomalyDetector.Stop()

	mainLogger.Info("Запуск прокси-клиента...")

	// Адаптеры для дочерних компонентов
	proxyLogger := eventlog.NewLogger(appLogger, evLog, "proxy")
	xrayLogger := eventlog.NewLogger(appLogger, evLog, "xray")
	monitorLogger := eventlog.NewLogger(appLogger, evLog, "monitor")

	// 1. Прокси-менеджер
	proxyManager := proxy.NewManager(proxyLogger)
	proxyConfig := proxy.Config{
		Address:  api.DefaultProxyAddress,
		Override: "<local>",
	}

	// 2–3. Убиваем осиротевший sing-box и ждём освобождения wintun kernel-объекта.
	//
	// Проблема "Cannot create a file when that file already exists":
	//   wintun создаёт именованный kernel-объект \Device\WINTUN-{GUID}.
	//   Объект живёт после смерти sing-box ещё 30-60 секунд (Windows GC асинхронный).
	//   sc stop wintun не работает — sing-box грузит wintun.dll напрямую, без SCM.
	//
	// РЕШЕНИЕ: после Remove-NetAdapter опрашиваем TCP/IP стек через netsh каждые 500мс.
	// Как только интерфейс исчезает из netsh — wintun kernel-объект освобождён.
	// Максимальный timeout — 70с. Обычно занимает < 5с если адаптер уже удалён.
	if killOrphanSingBox(mainLogger) {
		wintun.RecordStop()
	}
	wintun.RemoveStaleTunAdapter(mainLogger)
	wintun.PollUntilFree(mainLogger, config.TunInterfaceName)
	mainLogger.Info("Запуск sing-box...")
	routingCfg, err := config.LoadRoutingConfig(dataDir + "/routing.json")
	if err != nil {
		mainLogger.Warn("Не удалось загрузить routing config: %v, используем дефолтный", err)
		routingCfg = config.DefaultRoutingConfig()
	}
	if err := config.GenerateSingBoxConfig(secretFile, "config.singbox.json", routingCfg); err != nil {
		// Проверяем: возможно secret.key не заполнен (остался плейсхолдер)
		secretData, readErr := os.ReadFile(secretFile)
		if readErr != nil || len(secretData) == 0 {
			return fmt.Errorf("файл %s не найден или пуст — вставьте вашу VLESS-ссылку", secretFile)
		}
		// Проверка на незаполненный плейсхолдер (все строки начинаются с #)
		allComments := true
		for _, line := range strings.Split(strings.TrimSpace(string(secretData)), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				allComments = false
				break
			}
		}
		if allComments {
			return fmt.Errorf("файл %s содержит только комментарии — вставьте вашу VLESS-ссылку (vless://...)", secretFile)
		}
		mainLogger.Warn("Не удалось сгенерировать sing-box конфиг: %v", err)
		mainLogger.Warn("Sing-box будет запущен с СУЩЕСТВУЮЩИМ конфигом")
	}

	// xrayManager объявляем заранее чтобы OnCrash мог ссылаться на него (closure).
	var xrayManager xray.Manager
	xrayCfg := xray.Config{
		ExecutablePath: "./sing-box.exe",
		ConfigPath:     "config.singbox.json",
		SecretKeyPath:  secretFile,
		Args:           []string{"run", "--disable-color"}, // suppress ANSI color codes in log file
		Logger:         xrayLogger,
		SingBoxWriter:  eventlog.NewLineWriter(evLog, "sing-box", eventlog.LevelInfo),
		FileWriter:     output, // sing-box stderr → proxy-client.log (crash reasons visible)
		// BUG FIX: если sing-box падает сам (например из-за невалидного конфига),
		// отключаем системный прокси — иначе весь трафик уходит в мёртвый порт.
		OnCrash: func(crashErr error) {
			// Rate limiting — аналог nekoray coreRestartTimer.
			// Если ≥3 краша за 2 минуты — прекращаем авторестарты (core exits too frequently).
			if xray.IsTooManyRestarts(crashErr) {
				mainLogger.Error("[Error] Core exits too frequently — авторестарт отключён")
				notification.Send("Proxy — ошибка", "Частые сбои sing-box. Откройте приложение.")
				proxyManager.Disable() //nolint
				tray.SetEnabled(false)
				return
			}

			// Детектируем специфическую ошибку wintun kernel-объекта.
			// "Cannot create a file when that file already exists" означает что
			// предыдущий wintun\Device\WINTUN0 ещё не освобождён ядром ОС.
			// Kernel GC занимает 15-30 секунд — ждём и перезапускаем автоматически.
			output := xrayManager.LastOutput()
			isTunConflict := strings.Contains(output, "Cannot create a file when that file already exists") ||
				strings.Contains(output, "configure tun interface")
			if isTunConflict {
				// sing-box упал из-за wintun конфликта.
				// Не ждём фиксированное время — активно опрашиваем TCP/IP стек.
				// Remove-NetAdapter + polling до реального исчезновения интерфейса.
				wintun.RecordStop()
				mainLogger.Warn("Detected wintun conflict — ждём освобождения wintun kernel-объекта...")
				notification.Send("Proxy", "Перезапуск TUN...")
				wintun.RemoveStaleTunAdapter(mainLogger)
				wintun.PollUntilFree(mainLogger, config.TunInterfaceName)
				mainLogger.Info("Перезапуск sing-box после wintun GC...")
				if startErr := xrayManager.Start(); startErr != nil {
					mainLogger.Error("Не удалось перезапустить sing-box: %v", startErr)
					notification.Send("Proxy — ошибка", "Не удалось перезапустить. Откройте приложение.")
					proxyManager.Disable() //nolint
					tray.SetEnabled(false)
				} else {
					mainLogger.Info("sing-box успешно перезапущен (PID: %d)", xrayManager.GetPID())
					notification.Send("Proxy", "Перезапущен успешно ✓")
				}
				return
			}
			// Другие ошибки — не перезапускаем, уведомляем пользователя
			mainLogger.Error("sing-box упал неожиданно (%v) — отключаем системный прокси", crashErr)
			notification.Send("Proxy — ошибка", "sing-box упал. Проверьте лог и перезапустите.")
			if disableErr := proxyManager.Disable(); disableErr != nil {
				mainLogger.Error("Не удалось отключить прокси после краша sing-box: %v", disableErr)
			} else {
				tray.SetEnabled(false)
				mainLogger.Warn("Системный прокси отключён из-за краша sing-box. Проверьте data/routing.json и перезапустите.")
			}
		},
	}
	xrayManager, err = xray.NewManager(xrayCfg)
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
	quit := make(chan struct{})

	apiServer := api.NewServer(api.Config{
		ListenAddress: apiListenAddress,
		XRayManager:   xrayManager,
		ProxyManager:  proxyManager,
		ConfigPath:    runtimeFile,
		Logger:        mainLogger,
		EventLog:      evLog,
		QuitChan:      quit, // POST /api/quit closes this → graceful shutdown
	})
	apiServer.SetupTunRoutes(xrayCfg)
	apiServer.SetupFeatureRoutes()
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
				notification.Send("Proxy", "Прокси включён ✓")
			}
		},
		OnDisable: func() {
			if err := proxyManager.Disable(); err != nil {
				mainLogger.Error("Ошибка отключения прокси: %v", err)
			} else {
				tray.SetEnabled(false)
				mainLogger.Info("Прокси отключён через трей")
				notification.Send("Proxy", "Прокси отключён")
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
	// Фиксируем время остановки.
	wintun.RecordStop()
	// Убираем TUN адаптер при выходе — иначе wintun остаётся в системе
	mainLogger.Info("Очистка TUN адаптера при выходе...")
	wintun.Shutdown(mainLogger)
	if err := proxyManager.Disable(); err != nil {
		mainLogger.Error("Ошибка при отключении прокси: %v", err)
	}

	mainLogger.Info("Работа завершена корректно")
	return nil
}

// killOrphanSingBox убивает все sing-box.exe кроме текущего.
// Возвращает true если хотя бы один процесс был убит —
// вызывающий код должен подождать освобождения wintun handles.
func killOrphanSingBox(log logger.Logger) bool {
	return killProcessesByName("sing-box.exe", log)
}

// killProcessesByName убивает все процессы с указанным именем.
// Вынесена отдельно для тестируемости: тесты вызывают с реальным именем
// тестового subprocess вместо "sing-box.exe".
func killProcessesByName(targetName string, log logger.Logger) bool {
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

	procProcess32First := syscall.MustLoadDLL("kernel32.dll").MustFindProc("Process32FirstW")
	procProcess32Next := syscall.MustLoadDLL("kernel32.dll").MustFindProc("Process32NextW")

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
	kernel32 := syscall.MustLoadDLL("kernel32.dll")
	create := kernel32.MustFindProc("CreateToolhelp32Snapshot")
	const TH32CS_SNAPPROCESS = 0x00000002
	h, _, err := create.Call(TH32CS_SNAPPROCESS, 0)
	if h == uintptr(syscall.InvalidHandle) {
		return syscall.InvalidHandle, err
	}
	return syscall.Handle(h), nil
}

// isRunningAsAdmin возвращает true если процесс запущен с правами администратора.
// Используется для проверки перед запуском sing-box (TUN требует прав админа).
func isRunningAsAdmin() bool {
	// Пробуем открыть \\\\.\\PHYSICALDRIVE0 — доступно только администраторам
	f, err := os.Open("\\\\.\\PHYSICALDRIVE0")
	if err == nil {
		_ = f.Close()
		return true
	}
	return false
}

// createSingleInstanceMutex создаёт именованный мьютекс Windows.
// Возвращает ошибку если мьютекс уже занят (другой экземпляр запущен).
func createSingleInstanceMutex(name string) (syscall.Handle, error) {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	createMutex := kernel32.NewProc("CreateMutexW")

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
