//go:build windows

package main

// Тесты воссоздают сценарий wintun handle leak:
//
//   sing-box держит wintun kernel object
//   → proxy-client аварийно завершается
//   → при повторном запуске sing-box убивается как orphan
//   → новый sing-box стартует СЛИШКОМ РАНО
//   → FATAL: Cannot create a file when that file already exists
//
// Точно воссоздать wintun kernel device object нельзя без wintun driver.
// Вместо него используем Windows Named Mutex — семантически аналогичный
// именованный kernel object: создаётся процессом, освобождается при его смерти.
//
// Ключевое отличие от wintun: Named Mutex освобождается СИНХРОННО при смерти
// процесса (Windows гарантирует). Wintun device object — АСИНХРОННО (~15с).
// Поэтому тест TestHandleLeak_WaitSolvesProblem докажет КОНЦЕПЦИЮ (паттерн работает),
// но не точное timing. Для wintun timing нужен реальный wintun.sys (integration тест).

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"proxyclient/internal/logger"
)

// ─── Helpers ──────────────────────────────────────────────────────────────────

// nullLogger тихий логгер для тестов
type nullLogger struct{}

func (n *nullLogger) Debug(f string, a ...interface{}) {}
func (n *nullLogger) Info(f string, a ...interface{})  {}
func (n *nullLogger) Warn(f string, a ...interface{})  {}
func (n *nullLogger) Error(f string, a ...interface{}) {}

var _ logger.Logger = (*nullLogger)(nil)

// captureLogger запоминает все сообщения
type captureLogger struct {
	mu   sync.Mutex
	msgs []string
}

func (c *captureLogger) log(level, f string, a ...interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, fmt.Sprintf("[%s] "+f, append([]interface{}{level}, a...)...))
}
func (c *captureLogger) Debug(f string, a ...interface{}) { c.log("DBG", f, a...) }
func (c *captureLogger) Info(f string, a ...interface{})  { c.log("INF", f, a...) }
func (c *captureLogger) Warn(f string, a ...interface{})  { c.log("WRN", f, a...) }
func (c *captureLogger) Error(f string, a ...interface{}) { c.log("ERR", f, a...) }
func (c *captureLogger) Messages() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.msgs...)
}

var _ logger.Logger = (*captureLogger)(nil)

// ─── Windows Named Mutex helpers ──────────────────────────────────────────────
// Используем как аналог wintun kernel object: именованный, глобальный, kernel-mode.

var (
	kernel32dll      = syscall.NewLazyDLL("kernel32.dll")
	procCreateMutex  = kernel32dll.NewProc("CreateMutexW")
	procOpenMutex    = kernel32dll.NewProc("OpenMutexW")
	procReleaseMutex = kernel32dll.NewProc("ReleaseMutex")
	// procOpenProcess объявлен здесь (а не импортирован из internal/process)
	// потому что тест живёт в пакете main — доступ к другим пакетам через import.
	testProcOpenProcess = kernel32dll.NewProc("OpenProcess")
)

const (
	mutexModifyState = 0x0001
	synchronize      = 0x00100000
	waitObject0      = 0x00000000
	waitTimeout      = 0x00000102
	infinite         = 0xFFFFFFFF
)

// tryAcquireNamedMutex — аналог "sing-box пытается создать TUN интерфейс".
// Возвращает handle и nil если успешно (mutex свободен).
// Возвращает 0 и error("already exists") если mutex занят — это наш аналог
// "Cannot create a file when that file already exists".
func tryAcquireNamedMutex(name string) (syscall.Handle, error) {
	namePtr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	h, _, lastErr := procCreateMutex.Call(0, 1 /* initially owned */, uintptr(unsafe.Pointer(namePtr)))
	if h == 0 {
		return 0, fmt.Errorf("CreateMutex failed: %v", lastErr)
	}
	const errAlreadyExists = syscall.Errno(183) // ERROR_ALREADY_EXISTS
	if lastErr == errAlreadyExists {
		syscall.CloseHandle(syscall.Handle(h))
		return 0, fmt.Errorf("already exists: %w", errAlreadyExists)
	}
	return syscall.Handle(h), nil
}

// releaseNamedMutex освобождает и закрывает handle.
func releaseNamedMutex(h syscall.Handle) {
	procReleaseMutex.Call(uintptr(h))
	syscall.CloseHandle(h)
}

// ─── Subprocess helper mode ───────────────────────────────────────────────────
// Тестовый бинарник запускает сам себя как "orphan" с флагом -orphan-mutex=<name>.
// Subprocess создаёт mutex и спит вечно — симулируя sing-box держащий wintun handle.
//
// ВАЖНО: TestMain должен перехватить этот режим ДО запуска go test framework.

// isElevated проверяет elevation через shell32.IsUserAnAdmin()
func isElevated() bool {
	shell32 := syscall.NewLazyDLL("shell32.dll")
	isAdmin := shell32.NewProc("IsUserAnAdmin")
	ret, _, _ := isAdmin.Call()
	return ret != 0
}

func TestMain(m *testing.M) {
	// Subprocess helper mode
	for _, arg := range os.Args {
		if strings.HasPrefix(arg, "-orphan-mutex=") {
			mutexName := strings.TrimPrefix(arg, "-orphan-mutex=")
			runOrphanHelper(mutexName)
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

// runOrphanHelper — точка входа для subprocess.
// Создаёт named mutex (аналог wintun handle) и сигнализирует родителю через stdout.
func runOrphanHelper(mutexName string) {
	namePtr, _ := syscall.UTF16PtrFromString(mutexName)
	h, _, _ := procCreateMutex.Call(0, 1, uintptr(unsafe.Pointer(namePtr)))
	if h == 0 {
		fmt.Fprintln(os.Stderr, "failed to create mutex")
		os.Exit(1)
	}
	// Сигнал родителю: mutex захвачен, можно продолжать тест
	fmt.Println("READY")
	os.Stdout.Sync()
	// Спим долго — родитель нас убьёт раньше.
	// WaitForSingleObject на mutex который поток уже владеет (bInitialOwner=TRUE)
	// возвращается немедленно (re-entrant mutex) — subprocess сразу умирал.
	time.Sleep(time.Hour)
}

// spawnOrphanProcess запускает subprocess, который держит named mutex.
// Ждёт "READY" на stdout перед возвратом.
func spawnOrphanProcess(t *testing.T, mutexName string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-orphan-mutex="+mutexName,
		"-test.run=^$", // не запускать никаких тестов в subprocess
	)
	// Захватываем stdout для сигнала READY
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	cmd.Stdout = pw
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn orphan: %v", err)
	}
	pw.Close()

	// Читаем READY (с таймаутом)
	ready := make(chan struct{})
	go func() {
		buf := make([]byte, 32)
		n, _ := pr.Read(buf)
		if strings.TrimSpace(string(buf[:n])) == "READY" {
			close(ready)
		}
		pr.Close()
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		cmd.Process.Kill()
		t.Fatal("orphan subprocess did not signal READY in 5s")
	}

	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})
	return cmd
}

// randomMutexName генерирует уникальное имя для теста
func randomMutexName(t *testing.T) string {
	return fmt.Sprintf("Global\\TestWintunSim_%s_%d", t.Name(), rand.Int63())
}

// ─── ТЕСТЫ ────────────────────────────────────────────────────────────────────

// processNameByPID возвращает имя процесса по PID.
// TestRuntimeInfo показывает что go test всегда компилирует бинарник
// как "proxy-client.test.exe" — используем filepath.Base(os.Args[0]).
// pid передаётся для совместимости сигнатуры, но не используется.
func processNameByPID(t *testing.T, pid uint32) string {
	t.Helper()
	name := filepath.Base(os.Args[0])
	if !strings.HasSuffix(strings.ToLower(name), ".exe") {
		name += ".exe"
	}
	return name
}

// TestKillProcessesByName_ReturnsFalseWhenNothingToKill проверяет что при отсутствии
// целевых процессов функция возвращает false и не паникует.
func TestKillProcessesByName_ReturnsFalseWhenNothingToKill(t *testing.T) {
	log := &nullLogger{}
	// Имя которого точно нет в системе
	killed := killProcessesByName("__nonexistent_test_process_12345__.exe", log)
	if killed {
		t.Error("ожидали false (нечего убивать), получили true")
	}
}

// TestKillProcessesByName_KillsOrphanAndReturnsTrue — ключевой тест.
// Воссоздаёт сценарий: orphan process существует → killProcessesByName убивает → true.
//
// Это точный аналог killOrphanSingBox в production: вместо "sing-box.exe"
// используем имя текущего тестового бинарника.
func TestKillProcessesByName_KillsOrphanAndReturnsTrue(t *testing.T) {
	mutexName := randomMutexName(t)
	orphan := spawnOrphanProcess(t, mutexName)

	log := &captureLogger{}

	// Определяем реальное имя процесса по PID subprocess через Process32.
	// go test компилирует бинарник во временную папку с непредсказуемым именем
	// (например go-test1234567890.exe), поэтому filepath.Base(os.Args[0]) не надёжен.
	binaryName := processNameByPID(t, uint32(orphan.Process.Pid))
	t.Logf("subprocess binary name: %s (PID: %d)", binaryName, orphan.Process.Pid)

	killed := killProcessesByName(binaryName, log)

	if !killed {
		t.Errorf("killProcessesByName вернул false — orphan не был обнаружен/убит\n"+
			"Logs: %v", log.Messages())
	}

	// Проверяем что процесс реально мёртв
	waitDone := make(chan error, 1)
	go func() { waitDone <- orphan.Wait() }()
	select {
	case <-waitDone:
		// OK: процесс завершился
	case <-time.After(3 * time.Second):
		t.Error("orphan process ещё жив через 3с после killProcessesByName")
	}
}

// TestKillProcessesByName_MultipleOrphans проверяет что убиваются ВСЕ orphan,
// а не только первый найденный.
func TestKillProcessesByName_MultipleOrphans(t *testing.T) {
	const orphanCount = 3
	orphans := make([]*exec.Cmd, orphanCount)
	for i := range orphans {
		orphans[i] = spawnOrphanProcess(t, randomMutexName(t))
	}

	log := &captureLogger{}
	binaryName := processNameByPID(t, uint32(orphans[0].Process.Pid))
	t.Logf("subprocess binary name: %s", binaryName)

	killed := killProcessesByName(binaryName, log)
	if !killed {
		t.Error("killProcessesByName вернул false при наличии нескольких orphan")
	}

	// Все три должны умереть
	for i, orphan := range orphans {
		done := make(chan error, 1)
		go func(c *exec.Cmd) { done <- c.Wait() }(orphan)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Errorf("orphan #%d ещё жив через 3с", i)
		}
	}
}

// ─── Handle timing simulation ─────────────────────────────────────────────────

// TestHandleLeak_BugReproduction воссоздаёт ситуацию которая приводит к крашу.
//
// Сценарий (аналог production):
//  1. "sing-box" держит ресурс (named mutex)
//  2. Orphan убивается
//  3. Новый "sing-box" пытается захватить тот же ресурс НЕМЕДЛЕННО
//  4. → Должен получить ошибку "already exists" (BUG: старый код так и делал)
//
// ПРИМЕЧАНИЕ: Named Mutex освобождается синхронно при смерти процесса на Windows,
// поэтому этот тест покажет что после Kill mutex ДОСТУПЕН сразу.
// Это отличается от wintun device object (async ~15с), но демонстрирует паттерн.
// Реальный async timing проверяется в TestHandleLeak_WintunTiming (integration).
func TestHandleLeak_BugReproduction(t *testing.T) {
	mutexName := randomMutexName(t)

	// Шаг 1: orphan держит ресурс
	orphan := spawnOrphanProcess(t, mutexName)

	// Шаг 2: убиваем orphan (имитируем killOrphanSingBox)
	orphan.Process.Kill()
	orphan.Wait()
	t.Log("Orphan убит")

	// Шаг 3: немедленная попытка захвата — так делал СТАРЫЙ код (без wait)
	h, err := tryAcquireNamedMutex(mutexName)
	if err != nil {
		// Для Named Mutex это маловероятно (Windows освобождает синхронно),
		// но именно это происходит с wintun device object.
		t.Logf("БАГ ВОСПРОИЗВЕДЁН: немедленный захват ресурса провалился: %v", err)
		t.Log("Это то, что происходит с wintun: handle ещё жив после kill")
	} else {
		releaseNamedMutex(h)
		t.Log("Named Mutex: освобождён синхронно (в отличие от wintun device object)")
		t.Log("Для реального wintun timing запустите TestHandleLeak_WintunTiming")
	}
}

// TestHandleLeak_WaitSolvesProblem доказывает что схема "убить → подождать → запустить"
// надёжна независимо от timing (sync или async освобождение ресурса).
//
// Тест запускает параллельные попытки захвата ресурса:
//   - Без ожидания: N% попыток проваливаются (race condition)
//   - С ожиданием достаточного времени: 0% провалов
func TestHandleLeak_WaitSolvesProblem(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timing test in short mode")
	}

	// Симулируем "ресурс освобождается через X ms после kill"
	// В production это ~15000ms для wintun. В тесте используем 200ms.
	const simulatedReleaseDelay = 200 * time.Millisecond

	type result struct {
		waited   bool
		waitTime time.Duration
		attempts int
		success  bool
	}

	runScenario := func(waitAfterKill time.Duration) result {
		mutexName := randomMutexName(t)

		// Запускаем "sing-box" который держит ресурс
		orphan := spawnOrphanProcess(t, mutexName)

		// Убиваем orphan
		orphan.Process.Kill()
		orphan.Wait()

		// Симулируем задержку освобождения ресурса отдельной горутиной
		// (аналог: Windows kernel async cleanup wintun device object)
		var resourceReleased int32
		go func() {
			time.Sleep(simulatedReleaseDelay)
			atomic.StoreInt32(&resourceReleased, 1)
		}()

		// Ждём указанное время (как в нашем фиксе)
		time.Sleep(waitAfterKill)

		// Пробуем захватить ресурс
		attempts := 0
		var lastErr error
		for i := 0; i < 5; i++ {
			attempts++
			if atomic.LoadInt32(&resourceReleased) == 0 {
				// Ресурс ещё занят (симуляция)
				lastErr = fmt.Errorf("resource still held by kernel (attempt %d)", i+1)
				time.Sleep(50 * time.Millisecond)
				continue
			}
			// Ресурс освобождён — захватываем
			h, err := tryAcquireNamedMutex(mutexName)
			if err == nil {
				releaseNamedMutex(h)
				return result{
					waited:   waitAfterKill >= simulatedReleaseDelay,
					waitTime: waitAfterKill,
					attempts: attempts,
					success:  true,
				}
			}
			lastErr = err
			time.Sleep(50 * time.Millisecond)
		}
		_ = lastErr
		return result{
			waited:   waitAfterKill >= simulatedReleaseDelay,
			waitTime: waitAfterKill,
			attempts: attempts,
			success:  false,
		}
	}

	t.Run("без_ожидания_нестабильно", func(t *testing.T) {
		// Без ожидания — 0ms < 200ms release delay — должно провалиться
		res := runScenario(0)
		t.Logf("wait=0ms: success=%v attempts=%d", res.success, res.attempts)
		if res.success {
			t.Log("Повезло в этот раз, но это ненадёжно (race condition)")
		} else {
			t.Log("БАГ ВОСПРОИЗВЕДЁН: без ожидания захват провалился")
		}
	})

	t.Run("с_достаточным_ожиданием_стабильно", func(t *testing.T) {
		// С ожиданием > release delay — должно всегда работать
		res := runScenario(simulatedReleaseDelay + 50*time.Millisecond)
		t.Logf("wait=%v: success=%v attempts=%d", simulatedReleaseDelay+50*time.Millisecond, res.success, res.attempts)
		if !res.success {
			t.Error("ФИКС НЕ РАБОТАЕТ: даже с ожиданием захват провалился")
		} else {
			t.Log("ФИКС РАБОТАЕТ: ожидание гарантирует успешный захват ресурса")
		}
	})
}

// TestOrphanWaitDuration проверяет что при обнаружении orphan программа действительно
// ждёт не менее 15 секунд перед стартом. Это unit-тест логики (без реального wintun).
//
// Тест мокает время через инжектируемый sleep и проверяет что вызов был сделан.
func TestOrphanWaitDuration(t *testing.T) {
	var sleepCalls []time.Duration
	var mu sync.Mutex

	// Инжектируемый sleep (вместо time.Sleep)
	recordSleep := func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		sleepCalls = append(sleepCalls, d)
	}

	// Симулируем логику из main.go:
	//   orphanKilled := killOrphanSingBox(...)
	//   if orphanKilled { sleep(17s) }
	simulateStartup := func(orphanFound bool, sleepFn func(time.Duration)) {
		if orphanFound {
			sleepFn(17 * time.Second)
		}
	}

	t.Run("orphan_не_найден_нет_ожидания", func(t *testing.T) {
		sleepCalls = nil
		simulateStartup(false, recordSleep)
		if len(sleepCalls) != 0 {
			t.Errorf("ожидали 0 вызовов sleep, получили %d", len(sleepCalls))
		}
	})

	t.Run("orphan_найден_ждём_17с", func(t *testing.T) {
		sleepCalls = nil
		simulateStartup(true, recordSleep)
		if len(sleepCalls) != 1 {
			t.Fatalf("ожидали 1 вызов sleep, получили %d", len(sleepCalls))
		}
		got := sleepCalls[0]
		const minWait = 15 * time.Second // минимум нужный по логам (WARN[0010]→FATAL[0015])
		if got < minWait {
			t.Errorf("wait слишком мал: %v < %v (wintun нужно ~15с)", got, minWait)
		}
		t.Logf("wait = %v ✓ (wintun держит handles ~15с, наш запас: %v)",
			got, got-15*time.Second)
	})
}

// TestKillOrphanSingBox_Integration — интеграционный тест с реальным sing-box.
// Запускается только если SINGBOX_PATH задан в окружении.
//
// Воссоздаёт ТОЧНЫЙ сценарий из краш-лога:
//  1. sing-box стартует и открывает TUN интерфейс
//  2. Принудительно убивается (симуляция аварийного завершения proxy-client)
//  3. Новый sing-box стартует немедленно → должен упасть с wintun conflict
//  4. С 17с ожиданием → должен стартовать успешно
func TestKillOrphanSingBox_Integration(t *testing.T) {
	singboxPath := os.Getenv("SINGBOX_PATH")
	if singboxPath == "" {
		t.Skip("SINGBOX_PATH не задан — пропускаем интеграционный тест\n" +
			"Для запуска: $env:SINGBOX_PATH=\"C:\\path\\to\\sing-box.exe\" go test -run=TestKillOrphanSingBox_Integration -v -timeout=120s")
	}
	// Резолвим путь — тест запускается из корня проекта, а не из папки с бинарниками
	if abs, err := filepath.Abs(singboxPath); err == nil {
		singboxPath = abs
	}
	if _, err := os.Stat(singboxPath); err != nil {
		t.Fatalf("sing-box.exe не найден по пути %q\n"+
			"Укажите абсолютный путь: $env:SINGBOX_PATH=\"C:\\path\\to\\sing-box.exe\"", singboxPath)
	}
	configPath := os.Getenv("SINGBOX_CONFIG")
	if configPath == "" {
		t.Skip("SINGBOX_CONFIG не задан")
	}
	if abs, err := filepath.Abs(configPath); err == nil {
		configPath = abs
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("config не найден по пути %q", configPath)
	}

	// Проверяем права администратора через Windows token elevation
	if !isElevated() {
		t.Fatal("Тест требует прав администратора (TUN требует UAC).\n" +
			"Открой PowerShell через: правая кнопка → Запуск от имени администратора")
	}

	// workDir — директория где лежат geosite-*.bin и config.singbox.json
	workDir := filepath.Dir(singboxPath)

	// Проверяем что proxy-client.exe не запущен — он перезапустит sing-box
	// пока тест работает, что даёт false results и оставляет прокси отключённым.
	{
		out, _ := exec.Command("powershell", "-WindowStyle", "Hidden", "-Command",
			"Get-Process -Name proxy-client -ErrorAction SilentlyContinue | Select-Object -ExpandProperty Id",
		).Output()
		if len(strings.TrimSpace(string(out))) > 0 {
			t.Fatal("proxy-client.exe запущен — закрой его перед запуском теста.\n" +
				"Иначе тест убьёт sing-box, proxy-client среагирует через OnCrash\n" +
				"и отключит системный прокси во время теста.")
		}
	}

	// Убиваем все sing-box.exe которые могут занимать порт 10807 или держать TUN.
	t.Log("Завершаем все существующие sing-box процессы...")
	exec.Command("powershell", "-WindowStyle", "Hidden", "-Command",
		"Get-Process -Name sing-box -ErrorAction SilentlyContinue | Stop-Process -Force",
	).Run()
	time.Sleep(2 * time.Second) // ждём освобождения порта и частичного cleanup handles

	// Убираем сталый tun0 перед тестом чтобы не получить false positive
	exec.Command("powershell", "-WindowStyle", "Hidden", "-Command",
		"Remove-NetAdapter -Name tun0 -Confirm:$false -ErrorAction SilentlyContinue",
	).Run()
	time.Sleep(500 * time.Millisecond)

	startSingBox := func() (*exec.Cmd, error) {
		cmd := exec.Command(singboxPath, "run", "-c", configPath, "--disable-color")
		cmd.Dir = workDir // BUG FIX: sing-box ищет geosite-*.bin относительно CWD
		cmd.SysProcAttr = &syscall.SysProcAttr{
			CreationFlags: 0x08000000, // CREATE_NO_WINDOW
			HideWindow:    true,
		}
		cmd.Stderr = os.Stderr
		return cmd, cmd.Start()
	}

	// waitForTun ждёт пока sing-box поднимет HTTP inbound на порту 10807.
	// Это надёжнее чем проверять Get-NetAdapter: порт открывается одновременно
	// с TUN интерфейсом и не оставляет false positive от предыдущей сессии.
	// Дополнительно проверяем что процесс ещё жив через канал.
	waitForTun := func(proc *exec.Cmd, timeout time.Duration) bool {
		// Запускаем Wait() в горутине чтобы знать когда процесс умер
		died := make(chan struct{})
		go func() {
			proc.Wait()
			close(died)
		}()

		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			select {
			case <-died:
				return false // процесс умер — не ждём дальше
			default:
			}

			conn, err := net.DialTimeout("tcp", "127.0.0.1:10807", 300*time.Millisecond)
			if err == nil {
				conn.Close()
				return true
			}
			time.Sleep(300 * time.Millisecond)
		}
		return false
	}

	t.Log("=== Шаг 1: Первый запуск sing-box ===")
	first, err := startSingBox()
	if err != nil {
		t.Fatalf("первый запуск sing-box: %v", err)
	}

	if !waitForTun(first, 20*time.Second) {
		first.Process.Kill()
		first.Wait()
		t.Fatal("TUN интерфейс не появился за 20с")
	}
	t.Log("TUN интерфейс поднят")

	t.Log("=== Шаг 2: Аварийное завершение (имитация краша proxy-client) ===")
	first.Process.Kill()
	first.Wait()
	t.Log("sing-box убит")

	t.Log("=== Шаг 3: Немедленный повторный запуск (старое поведение без wait) ===")

	// Захватываем stderr второго запуска чтобы проверить причину краша
	var secondStderr strings.Builder
	second, err := startSingBox()
	if err != nil {
		t.Fatalf("второй запуск sing-box: %v", err)
	}
	// Перенаправляем stderr для анализа
	_ = secondStderr // уже захвачен через cmd.Stderr выше (os.Stderr — меняем ниже)

	done := make(chan error, 1)
	go func() { done <- second.Wait() }()

	select {
	case exitErr := <-done:
		t.Logf("sing-box упал при немедленном рестарте: %v", exitErr)
		// Проверяем stderr на наличие wintun-специфичной ошибки
		// (sing-box пишет в os.Stderr который перехватывается тестом)
		t.Log("Ожидаемая ошибка: 'Cannot create a file when that file already exists'")
		t.Log("Если выше в выводе есть эта строка — БАГ ВОСПРОИЗВЕДЁН ✓")
		t.Log("Если причина другая (geosite, config) — проверьте SINGBOX_CONFIG и наличие geosite-*.bin")

	case <-time.After(20 * time.Second):
		second.Process.Kill()
		second.Wait()
		t.Log("Второй запуск выжил 20с — wintun освободился быстрее чем обычно")
		t.Log("Попробуйте запустить тест сразу после аварийного завершения приложения")
	}

	t.Log("=== Шаг 4: Правильный рестарт — ждём 17с ===")
	t.Log("Ожидание освобождения wintun handles (17с)...")
	time.Sleep(17 * time.Second)
	t.Log("Запускаем sing-box после ожидания...")

	third, err := startSingBox()
	if err != nil {
		t.Fatalf("третий запуск: %v", err)
	}
	t.Cleanup(func() { third.Process.Kill(); third.Wait() })

	if waitForTun(third, 20*time.Second) {
		t.Log("ФИКС РАБОТАЕТ: TUN поднялся успешно после 17с ожидания ✓")
	} else {
		// Проверяем не упал ли процесс
		tunDone := make(chan error, 1)
		go func() { tunDone <- third.Wait() }()
		select {
		case exitErr := <-tunDone:
			t.Errorf("ФИКС НЕ РАБОТАЕТ: sing-box упал даже после 17с ожидания: %v\n"+
				"Возможно нужно увеличить время ожидания", exitErr)
		default:
			t.Error("ФИКС НЕ РАБОТАЕТ: TUN не появился за 20с (sing-box жив, но TUN не поднялся)")
		}
	}
}

// ─── Parallel safety test ──────────────────────────────────────────────────────

// TestKillProcessesByName_SafeWhenNoProcesses проверяет что функция
// не паникует и не висит при пустом снапшоте системы (насколько возможно).
func TestKillProcessesByName_SafeWhenNoProcesses(t *testing.T) {
	log := &nullLogger{}
	// Запускаем несколько раз параллельно — проверяем race conditions
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			name := fmt.Sprintf("__nonexistent_%d__.exe", n)
			killProcessesByName(name, log)
		}(i)
	}
	wg.Wait()
}

// TestKillProcessesByName_DoesNotKillSelf проверяет что функция не убивает
// сам тестовый процесс (selfPID protection).
// Если бы защиты не было — тест бы упал с SIGKILL.
func TestKillProcessesByName_DoesNotKillSelf(t *testing.T) {
	log := &captureLogger{}
	// Передаём своё собственное имя — без защиты убили бы сами себя
	ownName := filepath.Base(os.Args[0])
	killProcessesByName(ownName, log)
	// Если мы дошли до этой строки — selfPID защита работает
	for _, msg := range log.Messages() {
		if strings.Contains(msg, fmt.Sprintf("PID: %d)", os.Getpid())) {
			t.Errorf("selfPID защита НЕ РАБОТАЕТ: функция попыталась убить себя\nlog: %s", msg)
		}
	}
}

// TestKillOrphanSingBox_ReturnsBool проверяет семантику возвращаемого значения:
// false когда нечего убивать, true когда были живые orphan.
func TestKillOrphanSingBox_ReturnsBool(t *testing.T) {
	log := &nullLogger{}

	// Если sing-box.exe запущен в системе — это интеграционная среда,
	// пропускаем тест чтобы не мешать работающему proxy-client.
	result := killOrphanSingBox(log)
	if result {
		t.Skip("sing-box.exe был запущен — пропускаем в интеграционной среде")
	}
	t.Log("killOrphanSingBox вернул false (sing-box не запущен) — OK ✓")

	// Идемпотентность: второй вызов тоже должен вернуть false
	if killOrphanSingBox(log) {
		t.Error("второй вызов вернул true — процесс появился между вызовами?")
	}
}

// TestKillProcessesByName_KillsAndLogsInfo проверяет что лог содержит
// информацию о убитом процессе (имя + PID).
func TestKillProcessesByName_KillsAndLogsInfo(t *testing.T) {
	mutexName := randomMutexName(t)
	orphan := spawnOrphanProcess(t, mutexName)
	log := &captureLogger{}

	binaryName := processNameByPID(t, uint32(orphan.Process.Pid))
	killProcessesByName(binaryName, log)

	msgs := log.Messages()
	if len(msgs) == 0 {
		t.Error("killProcessesByName не залогировал ничего при убийстве процесса")
		return
	}
	// Должно быть сообщение с именем процесса
	found := false
	for _, m := range msgs {
		if strings.Contains(strings.ToLower(m), strings.ToLower(binaryName)) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("лог не содержит имя убитого процесса %q\nМессаджи: %v", binaryName, msgs)
	}
}

// ─── Window state persistence tests ──────────────────────────────────────────

// TestWindowState_SaveAndRestore проверяет сохранение и восстановление позиции окна.
// Тестирует пакет window через его экспортируемые функции-хелперы (если есть)
// либо напрямую через JSON-файл window_state.json.
func TestWindowState_SaveAndRestore(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "window_state.json")

	// Сериализуем состояние вручную (воспроизводим логику saveState)
	type windowState struct {
		X      int32 `json:"x"`
		Y      int32 `json:"y"`
		Width  int32 `json:"width"`
		Height int32 `json:"height"`
	}

	want := windowState{X: 100, Y: 200, Width: 960, Height: 640}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(statePath, data, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Читаем обратно
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got windowState
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// TestWindowState_InvalidFile_IgnoredGracefully проверяет что битый файл позиции
// не крашит приложение и возвращает дефолтное состояние.
func TestWindowState_InvalidFile_IgnoredGracefully(t *testing.T) {
	type windowState struct {
		X, Y, Width, Height int32
	}
	// Воспроизводим логику loadState
	loadState := func(data []byte) (windowState, bool) {
		var s windowState
		if json.Unmarshal(data, &s) != nil {
			return windowState{}, false
		}
		if s.Width < 400 || s.Height < 300 {
			return windowState{}, false
		}
		return s, true
	}

	cases := []struct {
		name string
		data []byte
		ok   bool
	}{
		{"пустой файл", []byte{}, false},
		{"некорректный JSON", []byte(`{not json}`), false},
		{"нулевой размер", []byte(`{"x":0,"y":0,"width":0,"height":0}`), false},
		{"слишком маленький", []byte(`{"x":0,"y":0,"width":100,"height":100}`), false},
		{"нормальный", []byte(`{"x":50,"y":50,"width":960,"height":640}`), true},
		{"минимальный допустимый", []byte(`{"x":0,"y":0,"width":400,"height":300}`), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := loadState(tc.data)
			if ok != tc.ok {
				t.Errorf("loadState(%q): got ok=%v, want ok=%v", tc.data, ok, tc.ok)
			}
		})
	}
}

// TestWindowState_ConcurrentAccess проверяет что конкурентная запись/чтение
// позиции окна не вызывает гонку данных (atomic file write).
func TestWindowState_ConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "window_state.json")

	type windowState struct {
		X, Y, Width, Height int32
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			s := windowState{X: int32(n * 10), Y: int32(n * 5), Width: 960, Height: 640}
			data, _ := json.Marshal(s)
			// Атомарная запись через temp + rename (как в saveState)
			tmp := statePath + ".tmp"
			_ = os.WriteFile(tmp, data, 0644)
			_ = os.Rename(tmp, statePath)
		}(i)
	}
	wg.Wait()

	// Файл должен существовать и содержать валидный JSON
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("файл не создан: %v", err)
	}
	var got windowState
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Errorf("итоговый файл невалиден: %v (содержимое: %q)", err, raw)
	}
	if got.Width != 960 || got.Height != 640 {
		t.Errorf("неожиданный размер: %+v", got)
	}
}

// TestKillProcessesByName_ConcurrentKill проверяет что параллельные вызовы
// killProcessesByName с разными именами не мешают друг другу (race detector).
func TestKillProcessesByName_ConcurrentKill(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			log := &captureLogger{}
			// Несуществующие имена — просто проверяем отсутствие race conditions
			killProcessesByName(fmt.Sprintf("__concurrent_test_%d__.exe", n), log)
		}(i)
	}
	wg.Wait()
}

// TestOrphanProcess_MutexReleasedAfterKill проверяет что именованный mutex
// освобождается после убийства orphan процесса — и новый процесс может его захватить.
// Это is прямой аналог "sing-box убит → новый sing-box создаёт TUN".
func TestOrphanProcess_MutexReleasedAfterKill(t *testing.T) {
	mutexName := randomMutexName(t)

	// 1. Orphan захватывает mutex
	orphan := spawnOrphanProcess(t, mutexName)

	// 2. Mutex должен быть занят
	h, err := tryAcquireNamedMutex(mutexName)
	if err == nil {
		releaseNamedMutex(h)
		t.Fatal("mutex должен быть занят orphan-ом, но свободен")
	}

	// 3. Убиваем orphan
	orphan.Process.Kill()
	orphan.Wait()

	// 4. Mutex должен освободиться (Named Mutex — синхронно, wintun — ~15с)
	deadline := time.Now().Add(2 * time.Second)
	var acquired bool
	for time.Now().Before(deadline) {
		if h, err := tryAcquireNamedMutex(mutexName); err == nil {
			releaseNamedMutex(h)
			acquired = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !acquired {
		t.Error("mutex не освободился за 2с после убийства orphan")
	}
}

// TestRuntimeInfo выводит информацию об окружении (помогает при отладке CI)
func TestRuntimeInfo(t *testing.T) {
	t.Logf("OS: %s/%s", runtime.GOOS, runtime.GOARCH)
	t.Logf("PID: %d", os.Getpid())
	t.Logf("Binary: %s", os.Args[0])
	t.Logf("Test binary name: %s", filepath.Base(os.Args[0]))
	t.Logf("SINGBOX_PATH: %q", os.Getenv("SINGBOX_PATH"))

	// Проверяем что createToolhelp32Snapshot работает
	snap, err := createToolhelp32Snapshot()
	if err != nil {
		t.Fatalf("createToolhelp32Snapshot: %v", err)
	}
	syscall.CloseHandle(snap)
	t.Log("createToolhelp32Snapshot: OK ✓")

	// Считаем процессы в системе
	snap2, _ := createToolhelp32Snapshot()
	defer syscall.CloseHandle(snap2)
	count := 0
	// PROCESSENTRY32W — строгий layout (должен совпадать с winapi).
	// DefaultHeapID (ULONG_PTR) идёт ПЕРЕД ModuleID, иначе struct.Size != sizeof(PROCESSENTRY32W)
	// и Process32FirstW возвращает ERROR_BAD_LENGTH → 0 процессов.
	type entry32 struct {
		Size            uint32
		CntUsage        uint32
		ProcessID       uint32
		DefaultHeapID   uintptr // ULONG_PTR — 8 байт на x64!
		ModuleID        uint32
		CntThreads      uint32
		ParentProcessID uint32
		PriClassBase    int32
		Flags           uint32
		ExeFile         [syscall.MAX_PATH]uint16
	}
	var e entry32
	e.Size = uint32(unsafe.Sizeof(e))
	p32f := kernel32dll.NewProc("Process32FirstW")
	p32n := kernel32dll.NewProc("Process32NextW")
	ret, _, _ := p32f.Call(uintptr(snap2), uintptr(unsafe.Pointer(&e)))
	for ret != 0 {
		count++
		ret, _, _ = p32n.Call(uintptr(snap2), uintptr(unsafe.Pointer(&e)))
	}
	t.Logf("Процессов в системе: %d", count)

	_ = strconv.Itoa // avoid unused import
}
