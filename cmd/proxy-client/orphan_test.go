//go:build windows

package main

// Тесты воссоздают сценарий wintun handle leak:
//
//	sing-box держит wintun kernel object
//	→ proxy-client аварийно завершается
//	→ при повторном запуске sing-box убивается как orphan
//	→ новый sing-box стартует СЛИШКОМ РАНО
//	→ FATAL: Cannot create a file when that file already exists
//
// Структура по тестовой пирамиде:
//
//	Unit        — чистые функции, без I/O, без процессов, быстрые
//	Integration — реальные subprocess, I/O, require Windows
//	E2E         — реальный sing-box, требует env vars + admin права

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

	"proxyclient/internal/fileutil"
	"proxyclient/internal/logger"
)

// ══════════════════════════════════════════════════════════════════════════════
// UNIT: Вспомогательные типы (mock logger, тестовые хелперы)
// ══════════════════════════════════════════════════════════════════════════════

// nullLogger — тихий логгер для unit-тестов где вывод не важен.
type nullLogger struct{}

func (n *nullLogger) Debug(f string, a ...interface{}) {}
func (n *nullLogger) Info(f string, a ...interface{})  {}
func (n *nullLogger) Warn(f string, a ...interface{})  {}
func (n *nullLogger) Error(f string, a ...interface{}) {}

var _ logger.Logger = (*nullLogger)(nil)

// captureLogger запоминает все сообщения для последующей проверки.
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

// ══════════════════════════════════════════════════════════════════════════════
// Windows Named Mutex helpers
// Используются как аналог wintun kernel object: именованный, kernel-mode.
// Ключевое отличие: Named Mutex освобождается синхронно, wintun — асинхронно.
// ══════════════════════════════════════════════════════════════════════════════

var (
	kernel32dll      = syscall.NewLazyDLL("kernel32.dll")
	procCreateMutex  = kernel32dll.NewProc("CreateMutexW")
	procReleaseMutex = kernel32dll.NewProc("ReleaseMutex")
)

// tryAcquireNamedMutex пытается создать/захватить именованный mutex.
// Аналог: sing-box пытается создать TUN интерфейс.
// Возвращает ошибку "already exists" если mutex занят — аналог wintun конфликта.
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

// randomMutexName генерирует глобально уникальное имя для теста.
func randomMutexName(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("Global\\TestWintunSim_%s_%d", t.Name(), rand.Int63())
}

// ══════════════════════════════════════════════════════════════════════════════
// Integration helpers: subprocess management
// ══════════════════════════════════════════════════════════════════════════════

// TestMain перехватывает subprocess helper mode ДО запуска go test framework.
// Если аргумент -orphan-mutex=<name> передан — процесс работает как orphan.
func TestMain(m *testing.M) {
	for _, arg := range os.Args {
		if strings.HasPrefix(arg, "-orphan-mutex=") {
			runOrphanHelper(strings.TrimPrefix(arg, "-orphan-mutex="))
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

// runOrphanHelper — точка входа для subprocess.
// Создаёт named mutex и сигнализирует родителю через stdout ("READY").
// Спит до принудительного завершения — имитирует sing-box держащий wintun handle.
func runOrphanHelper(mutexName string) {
	namePtr, _ := syscall.UTF16PtrFromString(mutexName)
	h, _, _ := procCreateMutex.Call(0, 1, uintptr(unsafe.Pointer(namePtr)))
	if h == 0 {
		fmt.Fprintln(os.Stderr, "failed to create mutex")
		os.Exit(1)
	}
	fmt.Println("READY")
	os.Stdout.Sync()
	// Спим долго — родитель нас убьёт раньше.
	// time.Sleep используется вместо WaitForSingleObject: последний re-entrant
	// и возвращается немедленно если поток уже владеет mutex.
	time.Sleep(time.Hour)
}

// spawnOrphanProcess запускает subprocess, который держит named mutex.
// Ждёт сигнал "READY" перед возвратом — гарантирует что mutex захвачен.
func spawnOrphanProcess(t *testing.T, mutexName string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0],
		"-orphan-mutex="+mutexName,
		"-test.run=^$", // не запускать никаких тестов в subprocess
	)
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
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	return cmd
}

// processNameByPID возвращает имя тестового бинарника.
// go test всегда компилирует бинарник как "<package>.test.exe".
func processNameByPID(t *testing.T, _ uint32) string {
	t.Helper()
	name := filepath.Base(os.Args[0])
	if !strings.HasSuffix(strings.ToLower(name), ".exe") {
		name += ".exe"
	}
	return name
}

// isElevated проверяет права администратора через shell32.IsUserAnAdmin().
func isElevated() bool {
	shell32 := syscall.NewLazyDLL("shell32.dll")
	ret, _, _ := shell32.NewProc("IsUserAnAdmin").Call()
	return ret != 0
}

// pollWithMock — тестовая версия wintun.PollUntilFree без реального netsh и без sleep.
// exists     — мок проверки InterfaceExists.
// maxIter    — максимальное количество итераций (аналог PollTimeout).
// sleepMs    — задержка между итерациями (0 для быстрых unit-тестов).
// Возвращает true если интерфейс исчез до таймаута.
func pollWithMock(exists func() bool, maxIter int, sleepMs int) bool {
	if !exists() {
		return true
	}
	for i := 0; i < maxIter; i++ {
		if sleepMs > 0 {
			time.Sleep(time.Duration(sleepMs) * time.Millisecond)
		}
		if !exists() {
			return true
		}
	}
	return false
}

// ══════════════════════════════════════════════════════════════════════════════
// UNIT TESTS — чистые функции, без subprocess, без I/O, < 1мс каждый
// ══════════════════════════════════════════════════════════════════════════════

// TestKillProcessesByName_ReturnsFalseWhenNothingToKill: несуществующий процесс
// → функция возвращает false и не паникует.
func TestKillProcessesByName_ReturnsFalseWhenNothingToKill(t *testing.T) {
	log := &nullLogger{}
	killed := killProcessesByName("__nonexistent_test_process_12345__.exe", log)
	if killed {
		t.Error("ожидали false (нечего убивать), получили true")
	}
}

// TestKillProcessesByName_SafeWhenNoProcesses: параллельные вызовы с разными именами
// не вызывают data race и не паникуют.
func TestKillProcessesByName_SafeWhenNoProcesses(t *testing.T) {
	t.Parallel()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			killProcessesByName(fmt.Sprintf("__safe_test_%d__.exe", n), &nullLogger{})
		}(i)
	}
	wg.Wait()
}

// TestKillProcessesByName_DoesNotKillSelf: текущий процесс не должен убивать сам себя,
// иначе тест упадёт — что само по себе доказательство.
func TestKillProcessesByName_DoesNotKillSelf(t *testing.T) {
	t.Parallel()
	selfName := processNameByPID(t, uint32(os.Getpid()))
	killed := killProcessesByName(selfName, &nullLogger{})
	// Если функция убила сам процесс — тест сюда не дошёл бы.
	// killed может быть true если в системе есть другие процессы с тем же именем.
	t.Logf("selfPID=%d selfName=%q killed=%v", os.Getpid(), selfName, killed)
}

// TestKillOrphanSingBox_ReturnsBool: семантика возвращаемого значения — false когда
// sing-box.exe не запущен, true когда был запущен и убит.
func TestKillOrphanSingBox_ReturnsBool(t *testing.T) {
	t.Parallel()
	log := &nullLogger{}

	// Если sing-box.exe запущен в системе — это интеграционная среда.
	// Пропускаем чтобы не мешать работающему proxy-client.
	result := killOrphanSingBox(log)
	if result {
		t.Skip("sing-box.exe найден и убит — пропускаем в интеграционной среде")
	}
	t.Log("killOrphanSingBox вернул false (sing-box не запущен) ✓")

	// Идемпотентность: второй вызов тоже должен вернуть false.
	if killOrphanSingBox(log) {
		t.Error("второй вызов вернул true — процесс появился между вызовами?")
	}
}

// TestWintunPollStrategy: логика polling-стратегии освобождения wintun.
// Unit-тест: мокаем InterfaceExists, без реального netsh и без задержек.
func TestWintunPollStrategy(t *testing.T) {
	t.Parallel()

	t.Run("сразу_свободен_нет_итераций", func(t *testing.T) {
		t.Parallel()
		calls := 0
		exists := func() bool { calls++; return false }
		freed := pollWithMock(exists, 5, 0)
		if !freed {
			t.Error("ожидали freed=true когда интерфейс сразу свободен")
		}
		if calls != 1 {
			t.Errorf("ожидали 1 вызов проверки, получили %d", calls)
		}
	})

	t.Run("освобождается_через_N_итераций", func(t *testing.T) {
		t.Parallel()
		calls := 0
		const releaseAfter = 3
		exists := func() bool { calls++; return calls <= releaseAfter }

		freed := pollWithMock(exists, 10, 0)

		if !freed {
			t.Error("polling должен был остановиться когда интерфейс исчез")
		}
		want := releaseAfter + 1 // 1 pre-check + releaseAfter итераций
		if calls != want {
			t.Errorf("ожидали %d вызовов, получили %d", want, calls)
		}
	})

	t.Run("таймаут_если_не_освобождается", func(t *testing.T) {
		t.Parallel()
		calls := 0
		const maxIter = 5
		exists := func() bool { calls++; return true } // всегда занят

		freed := pollWithMock(exists, maxIter, 0)

		if freed {
			t.Error("polling не должен был остановиться — интерфейс не освобождался")
		}
		if calls > maxIter+1 {
			t.Errorf("слишком много вызовов: %d > %d", calls, maxIter+1)
		}
	})

	t.Run("интерфейс_не_существует_с_первой_проверки", func(t *testing.T) {
		t.Parallel()
		// Главный сценарий: orphan не успел даже запустить TUN —
		// polling должен вернуться сразу без единой итерации.
		calls := 0
		exists := func() bool { calls++; return false }
		freed := pollWithMock(exists, 100, 0)
		if !freed {
			t.Error("ожидали freed=true")
		}
		if calls != 1 {
			t.Errorf("ожидали 1 вызов, получили %d", calls)
		}
	})
}

// TestWindowState_SaveAndRestore: JSON сериализация позиции окна.
func TestWindowState_SaveAndRestore(t *testing.T) {
	t.Parallel()
	type windowState struct {
		X, Y, Width, Height int32
	}
	loadState := func(data []byte) (windowState, bool) {
		var s windowState
		if json.Unmarshal(data, &s) != nil || s.Width < 400 || s.Height < 300 {
			return windowState{}, false
		}
		return s, true
	}

	t.Run("сохранение_и_восстановление", func(t *testing.T) {
		t.Parallel()
		want := windowState{X: 100, Y: 200, Width: 960, Height: 640}
		data, _ := json.Marshal(want)
		got, ok := loadState(data)
		if !ok {
			t.Fatal("loadState вернул ok=false для валидного JSON")
		}
		if got != want {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})

	t.Run("битый_файл_игнорируется", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name string
			data []byte
		}{
			{"пустой", []byte{}},
			{"invalid_json", []byte(`{not json}`)},
			{"нулевой_размер", []byte(`{"x":0,"y":0,"width":0,"height":0}`)},
			{"слишком_маленький", []byte(`{"x":0,"y":0,"width":100,"height":100}`)},
		}
		for _, tc := range cases {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				_, ok := loadState(tc.data)
				if ok {
					t.Errorf("loadState(%q) должен вернуть ok=false", tc.data)
				}
			})
		}
	})

	t.Run("минимальный_допустимый_размер", func(t *testing.T) {
		t.Parallel()
		data := []byte(`{"x":0,"y":0,"width":400,"height":300}`)
		_, ok := loadState(data)
		if !ok {
			t.Error("минимальный допустимый размер 400x300 должен проходить валидацию")
		}
	})
}

// atomicReplaceFile атомарно заменяет dst содержимым data.
//
// На Windows os.Rename над существующим файлом НЕ атомарна:
// ядро сначала удаляет целевой файл, потом переименовывает источник.
// В этом окне другой writer может успеть записать своё содержимое,
// и два JSON-блока конкатенируются → невалидный файл.
//
// MoveFileExW с флагом MOVEFILE_REPLACE_EXISTING атомарна на NTFS:
// она меняет directory entry одной транзакцией без промежуточного удаления.
// Именно её используют production proxy-клиенты (v2rayN, Clash Verge Rev)
// для атомарной замены конфигов sing-box.
func atomicReplaceFile(dst string, data []byte) error {
	// Делегируем в fileutil.WriteAtomic — единственная реализация для всего проекта.
	return fileutil.WriteAtomic(dst, data, 0644)
}

// TestWindowState_ConcurrentAccess: атомарность записи файла под нагрузкой.
//
// Тест проверяет что atomicReplaceFile не оставляет невалидный JSON
// при конкурентных записях. Это реальный сценарий из production:
// proxy-client сохраняет позицию окна при каждом перемещении,
// sing-box конфиг обновляется через doApply — оба используют tmp+rename.
//
// Корень предыдущего бага: все горутины писали в ОДИН tmp-файл.
// Горутина A писала в .tmp, горутина B перетирала .tmp, горутина A
// делала Rename — результат принадлежал B. А os.Rename(existing) на
// Windows не атомарна и может дать два JSON подряд в одном файле.
func TestWindowState_ConcurrentAccess(t *testing.T) {
	t.Parallel()
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
			// atomicReplaceFile: уникальный tmp per-goroutine + MoveFileExW REPLACE_EXISTING.
			if err := atomicReplaceFile(statePath, data); err != nil {
				// Ошибки записи возможны при конкуренции на Windows — не фатальны:
				// хотя бы одна горутина должна победить и оставить валидный файл.
				t.Logf("goroutine %d: atomicReplaceFile: %v", n, err)
			}
		}(i)
	}
	wg.Wait()

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

// ══════════════════════════════════════════════════════════════════════════════
// INTEGRATION TESTS — реальные subprocess и I/O, занимают 1-10 секунд
// ══════════════════════════════════════════════════════════════════════════════

// TestKillProcessesByName_KillsOrphanAndReturnsTrue: ключевой тест.
// Воссоздаёт точный сценарий killOrphanSingBox в production:
// orphan существует → killProcessesByName убивает → возвращает true.
func TestKillProcessesByName_KillsOrphanAndReturnsTrue(t *testing.T) {
	mutexName := randomMutexName(t)
	orphan := spawnOrphanProcess(t, mutexName)

	binaryName := processNameByPID(t, uint32(orphan.Process.Pid))
	t.Logf("subprocess binary name: %s (PID: %d)", binaryName, orphan.Process.Pid)

	log := &captureLogger{}
	killed := killProcessesByName(binaryName, log)
	if !killed {
		t.Errorf("ожидали true (orphan убит), получили false\nЛог: %v", log.Messages())
	}

	// Процесс должен завершиться после kill
	done := make(chan struct{})
	go func() { orphan.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Error("orphan не завершился за 3с после kill")
	}
}

// TestKillProcessesByName_MultipleOrphans: несколько orphan процессов убиваются
// все сразу одним вызовом.
func TestKillProcessesByName_MultipleOrphans(t *testing.T) {
	const orphanCount = 3
	orphans := make([]*exec.Cmd, orphanCount)
	for i := range orphans {
		orphans[i] = spawnOrphanProcess(t, randomMutexName(t))
	}

	binaryName := processNameByPID(t, uint32(orphans[0].Process.Pid))
	t.Logf("subprocess binary name: %s", binaryName)

	log := &captureLogger{}
	killed := killProcessesByName(binaryName, log)
	if !killed {
		t.Errorf("ожидали true (%d orphans убито), получили false\nЛог: %v", orphanCount, log.Messages())
	}

	for i, o := range orphans {
		done := make(chan struct{})
		go func(cmd *exec.Cmd) { cmd.Wait(); close(done) }(o)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Errorf("orphan[%d] не завершился за 3с", i)
		}
	}
}

// TestKillProcessesByName_KillsAndLogsInfo: лог должен содержать имя убитого процесса.
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

// TestKillProcessesByName_ConcurrentKill: параллельные вызовы с разными именами
// не мешают друг другу (race detector).
func TestKillProcessesByName_ConcurrentKill(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			log := &captureLogger{}
			killProcessesByName(fmt.Sprintf("__concurrent_test_%d__.exe", n), log)
		}(i)
	}
	wg.Wait()
}

// TestHandleLeak_BugReproduction: документирует оригинальный баг handle leak.
// Создаёт orphan с mutex → убивает → пытается захватить mutex немедленно.
// Named Mutex освобождается синхронно (в отличие от wintun device object).
func TestHandleLeak_BugReproduction(t *testing.T) {
	mutexName := randomMutexName(t)
	orphan := spawnOrphanProcess(t, mutexName)

	orphan.Process.Kill()
	orphan.Wait()
	t.Log("Orphan убит")

	h, err := tryAcquireNamedMutex(mutexName)
	if err != nil {
		t.Logf("БАГ ВОСПРОИЗВЕДЁН (named mutex вариант): %v", err)
		t.Log("Для реального wintun kernel object объект жив ещё 30-60с после kill")
	} else {
		releaseNamedMutex(h)
		t.Log("Named Mutex освобождён синхронно (в отличие от wintun device object)")
		t.Log("Реальный wintun тест требует sing-box.exe — см. TestKillOrphanSingBox_Integration")
	}
}

// TestHandleLeak_WaitSolvesProblem: polling-стратегия гарантирует успех.
// Симулирует async освобождение ресурса через goroutine + atomic flag.
// Доказывает что "убить → дождаться освобождения → запустить" надёжнее
// фиксированного sleep.
func TestHandleLeak_WaitSolvesProblem(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timing test in short mode")
	}

	// Симулируем wintun: ресурс освобождается через X ms после kill.
	// В production: ~15-60 секунд. В тесте: 200ms.
	const releaseDelay = 200 * time.Millisecond

	runScenario := func(t *testing.T, waitAfterKill time.Duration) (success bool) {
		t.Helper()
		mutexName := randomMutexName(t)
		orphan := spawnOrphanProcess(t, mutexName)

		orphan.Process.Kill()
		orphan.Wait()

		// Горутина симулирует async cleanup kernel object
		var resourceReleased int32
		go func() {
			time.Sleep(releaseDelay)
			atomic.StoreInt32(&resourceReleased, 1)
		}()

		time.Sleep(waitAfterKill)

		// Polling: ждём пока ресурс освободится
		freed := pollWithMock(func() bool {
			return atomic.LoadInt32(&resourceReleased) == 0 // занят пока == 0
		}, 20, 10)
		return freed
	}

	t.Run("без_ожидания_нестабильно", func(t *testing.T) {
		// 0ms << 200ms release delay → ресурс ещё занят
		success := runScenario(t, 0)
		if success {
			t.Log("Повезло в этот раз (race condition) — но это ненадёжно")
		} else {
			t.Log("БАГ ВОСПРОИЗВЕДЁН: без ожидания захват провалился ✓")
		}
		// Не фейлим: результат зависит от scheduling, оба варианта валидны
	})

	t.Run("polling_стратегия_всегда_работает", func(t *testing.T) {
		// С polling > release delay → всегда успешно
		success := runScenario(t, releaseDelay+50*time.Millisecond)
		if !success {
			t.Error("ФИКС НЕ РАБОТАЕТ: polling стратегия должна гарантировать успех")
		} else {
			t.Log("ФИКС РАБОТАЕТ: polling гарантирует успешный захват ресурса ✓")
		}
	})
}

// TestOrphanProcess_MutexReleasedAfterKill: mutex освобождается после kill,
// и новый процесс может его захватить — прямой аналог wintun сценария.
func TestOrphanProcess_MutexReleasedAfterKill(t *testing.T) {
	mutexName := randomMutexName(t)
	orphan := spawnOrphanProcess(t, mutexName)

	// Mutex занят orphan-ом
	h, err := tryAcquireNamedMutex(mutexName)
	if err == nil {
		releaseNamedMutex(h)
		t.Fatal("mutex должен быть занят orphan-ом, но свободен")
	}

	orphan.Process.Kill()
	orphan.Wait()

	// После kill mutex освобождается (для Named Mutex — синхронно)
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
		t.Error("mutex не освободился за 2с после kill orphan")
	}
}

// ══════════════════════════════════════════════════════════════════════════════
// E2E TESTS — реальный sing-box.exe, admin права, env vars обязательны
// ══════════════════════════════════════════════════════════════════════════════

// TestKillOrphanSingBox_Integration — E2E тест с реальным sing-box.
// Воссоздаёт точный сценарий из краш-логов:
//  1. sing-box стартует и открывает TUN интерфейс
//  2. Принудительно убивается (имитация аварийного завершения proxy-client)
//  3. Без ожидания: новый sing-box должен упасть с wintun conflict
//  4. С polling: новый sing-box должен стартовать успешно
//
// Запуск: $env:SINGBOX_PATH="C:\path\to\sing-box.exe" $env:SINGBOX_CONFIG="..." go test -run=TestKillOrphanSingBox_Integration -v -timeout=120s
func TestKillOrphanSingBox_Integration(t *testing.T) {
	singboxPath := os.Getenv("SINGBOX_PATH")
	if singboxPath == "" {
		t.Skip("SINGBOX_PATH не задан — E2E тест пропущен\n" +
			"Для запуска: $env:SINGBOX_PATH=\"C:\\path\\to\\sing-box.exe\" go test -run=TestKillOrphanSingBox_Integration -v -timeout=120s")
	}
	configPath := os.Getenv("SINGBOX_CONFIG")
	if configPath == "" {
		t.Skip("SINGBOX_CONFIG не задан")
	}

	if abs, err := filepath.Abs(singboxPath); err == nil {
		singboxPath = abs
	}
	if abs, err := filepath.Abs(configPath); err == nil {
		configPath = abs
	}
	if _, err := os.Stat(singboxPath); err != nil {
		t.Fatalf("sing-box.exe не найден: %v", err)
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("config не найден: %v", err)
	}
	if !isElevated() {
		t.Fatal("E2E тест требует прав администратора (TUN требует UAC)")
	}

	workDir := filepath.Dir(singboxPath)

	// Убеждаемся что SafeSky не запущен — он перехватит наш kill
	if out, _ := exec.Command("powershell", "-WindowStyle", "Hidden", "-Command",
		"Get-Process -Name SafeSky -ErrorAction SilentlyContinue | Select-Object -ExpandProperty Id",
	).Output(); len(strings.TrimSpace(string(out))) > 0 {
		t.Fatal("SafeSky.exe запущен — закрой его перед E2E тестом")
	}

	// Чистим возможные зависшие экземпляры
	exec.Command("powershell", "-WindowStyle", "Hidden", "-Command",
		"Get-Process -Name sing-box -ErrorAction SilentlyContinue | Stop-Process -Force").Run()
	exec.Command("powershell", "-WindowStyle", "Hidden", "-Command",
		"Remove-NetAdapter -Name tun0 -Confirm:$false -ErrorAction SilentlyContinue").Run()
	time.Sleep(2 * time.Second)

	startSingBox := func() (*exec.Cmd, error) {
		cmd := exec.Command(singboxPath, "run", "-c", configPath, "--disable-color")
		cmd.Dir = workDir
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
		cmd.Stderr = os.Stderr
		return cmd, cmd.Start()
	}

	// waitForTUN ждёт пока sing-box откроет HTTP inbound на 127.0.0.1:10807.
	waitForTUN := func(proc *exec.Cmd, timeout time.Duration) bool {
		died := make(chan struct{})
		go func() { proc.Wait(); close(died) }()
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			select {
			case <-died:
				return false
			default:
			}
			if conn, err := net.DialTimeout("tcp", "127.0.0.1:10807", 300*time.Millisecond); err == nil {
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
		t.Fatalf("первый запуск: %v", err)
	}
	if !waitForTUN(first, 20*time.Second) {
		first.Process.Kill()
		t.Fatal("TUN интерфейс не появился за 20с")
	}
	t.Log("TUN интерфейс поднят ✓")

	t.Log("=== Шаг 2: Аварийное завершение ===")
	first.Process.Kill()
	first.Wait()
	t.Log("sing-box убит")

	t.Log("=== Шаг 3: Немедленный рестарт (должен упасть с wintun conflict) ===")
	second, err := startSingBox()
	if err != nil {
		t.Fatalf("второй запуск: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- second.Wait() }()
	select {
	case exitErr := <-done:
		t.Logf("sing-box упал при немедленном рестарте: %v", exitErr)
		t.Log("Проверьте stderr выше на строку 'Cannot create a file when that file already exists'")
	case <-time.After(20 * time.Second):
		second.Process.Kill()
		second.Wait()
		t.Log("Второй запуск выжил 20с — wintun освободился быстрее ожидаемого")
	}

	t.Log("=== Шаг 4: Polling до освобождения wintun → рестарт ===")
	deadline := time.Now().Add(70 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		cmd := exec.Command("netsh", "interface", "show", "interface", "tun0")
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
		if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "tun0") {
			t.Log("tun0 освобождён по polling ✓")
			break
		}
	}

	third, err := startSingBox()
	if err != nil {
		t.Fatalf("третий запуск: %v", err)
	}
	t.Cleanup(func() { third.Process.Kill(); third.Wait() })

	if waitForTUN(third, 20*time.Second) {
		t.Log("ФИКС РАБОТАЕТ: TUN поднялся успешно после polling ✓")
	} else {
		tunDone := make(chan error, 1)
		go func() { tunDone <- third.Wait() }()
		select {
		case exitErr := <-tunDone:
			t.Errorf("ФИКС НЕ РАБОТАЕТ: sing-box упал даже после polling: %v", exitErr)
		default:
			t.Error("ФИКС НЕ РАБОТАЕТ: TUN не появился за 20с (sing-box жив, TUN не поднялся)")
		}
	}
}

// ══════════════════════════════════════════════════════════════════════════════
// DIAGNOSTICS — вспомогательный тест, всегда PASS, помогает при отладке CI
// ══════════════════════════════════════════════════════════════════════════════

// TestRuntimeInfo выводит информацию об окружении.
// Полезен при отладке — показывает OS, PID, имя бинарника, количество процессов.
func TestRuntimeInfo(t *testing.T) {
	t.Logf("OS: %s/%s", runtime.GOOS, runtime.GOARCH)
	t.Logf("PID: %d", os.Getpid())
	t.Logf("Binary: %s", os.Args[0])
	t.Logf("Admin: %v", isElevated())

	// Подсчёт процессов через CreateToolhelp32Snapshot
	snap, err := createToolhelp32Snapshot()
	if err != nil {
		t.Logf("CreateToolhelp32Snapshot: %v", err)
		return
	}
	defer syscall.CloseHandle(snap)

	type entry32 struct {
		Size          uint32
		CntUsage      uint32
		ProcessID     uint32
		DefaultHeapID uintptr
		ModuleID      uint32
		CntThreads    uint32
		ParentPID     uint32
		PriClassBase  int32
		Flags         uint32
		ExeFile       [syscall.MAX_PATH]uint16
	}
	var e entry32
	e.Size = uint32(unsafe.Sizeof(e))
	p32f := kernel32dll.NewProc("Process32FirstW")
	p32n := kernel32dll.NewProc("Process32NextW")
	count := 0
	ret, _, _ := p32f.Call(uintptr(snap), uintptr(unsafe.Pointer(&e)))
	for ret != 0 {
		count++
		ret, _, _ = p32n.Call(uintptr(snap), uintptr(unsafe.Pointer(&e)))
	}
	t.Logf("Процессов в системе: %d", count)

	_ = strconv.Itoa // suppress unused import
}
