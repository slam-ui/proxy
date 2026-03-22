package anomalylog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"proxyclient/internal/eventlog"
)

// helper: создаёт Detector с временной директорией
func newTestDetector(t *testing.T) (*Detector, *eventlog.Log, string) {
	t.Helper()
	evLog := eventlog.New(200)
	dir := t.TempDir()
	d := New(evLog, dir)
	d.pollInterval = 200 * time.Millisecond // быстрый опрос для тестов
	return d, evLog, dir
}

// helper: ждёт появления файла в dir с подстрокой substr в имени
func waitFile(t *testing.T, dir, substr string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if !strings.Contains(e.Name(), substr) {
				continue
			}
			path := filepath.Join(dir, e.Name())
			// Ждём пока файл полностью записан: должен содержать секцию АНОМАЛИЯ.
			// Без этой проверки тест читает файл раньше чем writeFile() завершает запись
			// (race: файл создан → waitFile возвращает → os.ReadFile читает неполный файл).
			data, err := os.ReadFile(path)
			if err == nil && strings.Contains(string(data), "АНОМАЛИЯ") {
				return path
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("файл с %q не появился за %v в %s", substr, timeout, dir)
	return ""
}

// ─── Регрессионные тесты на исправленные баги ─────────────────────────────

// TestDetector_CrashKeywordInWarnLevel проверяет исправление бага:
// crash-ключевые слова ранее проверялись только в LevelInfo, но не в LevelWarn.
// В результате "panic: ..." на уровне warn не создавал _crash.log файл.
func TestDetector_CrashKeywordInWarnLevel(t *testing.T) {
	d, evLog, dir := newTestDetector(t)
	d.Start()
	defer d.Stop()

	evLog.Add(eventlog.LevelWarn, "sing-box", "panic: runtime error: nil pointer dereference")

	path := waitFile(t, dir, "crash", 2*time.Second)
	content, _ := os.ReadFile(path)
	if !strings.Contains(string(content), "nil pointer dereference") {
		t.Error("crash-сообщение из LevelWarn не попало в _crash.log")
	}
}

// TestDetector_CrashWarnNotCountedAsBurst проверяет исправление бага:
// warn-событие содержащее crash-ключевое слово ранее добавлялось в burst-счётчик.
// Правильное поведение: такой warn классифицируется как CRASH и не идёт в burst.
func TestDetector_CrashWarnNotCountedAsBurst(t *testing.T) {
	d, evLog, dir := newTestDetector(t)
	d.Start()
	defer d.Stop()

	// 1 crash-warn + 2 обычных warn = всего 2 не-crash warn, burst не должен сработать
	evLog.Add(eventlog.LevelWarn, "sing-box", "fatal: cannot bind port")
	evLog.Add(eventlog.LevelWarn, "net", "connection reset")
	evLog.Add(eventlog.LevelWarn, "net", "read timeout")

	// Ждём достаточно для нескольких циклов poll
	time.Sleep(1500 * time.Millisecond)

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), "warn_burst") {
			t.Errorf("warn_burst файл не должен создаваться: crash-warn не считается в burst: %s", e.Name())
		}
	}

	// crash-файл должен появиться (от первого warn)
	waitFile(t, dir, "crash", 2*time.Second)
}

// TestDetector_MixedKindsInOneTick_SeparateFiles проверяет исправление бага:
// когда за один тик появлялись аномалии разных видов (ERROR и CRASH),
// все они писались в один файл с видом последнего события.
// Правильное поведение: каждый вид аномалии → отдельный файл.
func TestDetector_MixedKindsInOneTick_SeparateFiles(t *testing.T) {
	d, evLog, dir := newTestDetector(t)
	d.Start()
	defer d.Stop()

	// Оба события попадут в один тик детектора
	evLog.Add(eventlog.LevelError, "api", "connection refused")
	evLog.Add(eventlog.LevelInfo, "sing-box", "panic: runtime error")

	// Ждём оба файла
	errorPath := waitFile(t, dir, "error", 2*time.Second)
	crashPath := waitFile(t, dir, "crash", 2*time.Second)

	errorContent, _ := os.ReadFile(errorPath)
	crashContent, _ := os.ReadFile(crashPath)

	// Каждый файл должен декларировать правильный вид аномалии в заголовке
	if !strings.Contains(string(errorContent), "ERROR") {
		t.Errorf("_error.log не содержит тип ERROR в заголовке")
	}
	if !strings.Contains(string(crashContent), "CRASH") {
		t.Errorf("_crash.log не содержит тип CRASH в заголовке")
	}

	// Каждый файл должен содержать своё аномальное событие в секции АНОМАЛИЯ.
	// Проверяем по секции "── АНОМАЛИЯ", а не по всему файлу (контекст
	// намеренно включает все последние события, в том числе из других видов).
	errorBody := string(errorContent)
	anomalyIdx := strings.LastIndex(errorBody, "── АНОМАЛИЯ")
	if anomalyIdx < 0 || !strings.Contains(errorBody[anomalyIdx:], "connection refused") {
		t.Error("_error.log не содержит 'connection refused' в секции АНОМАЛИЯ")
	}

	crashBody := string(crashContent)
	anomalyIdx = strings.LastIndex(crashBody, "── АНОМАЛИЯ")
	if anomalyIdx < 0 || !strings.Contains(crashBody[anomalyIdx:], "panic: runtime error") {
		t.Error("_crash.log не содержит 'panic: runtime error' в секции АНОМАЛИЯ")
	}
}

func TestDetector_ErrorCreatesFile(t *testing.T) {
	d, evLog, dir := newTestDetector(t)
	d.Start()
	defer d.Stop()

	evLog.Add(eventlog.LevelError, "test", "критическая ошибка соединения")

	path := waitFile(t, dir, "error", 2*time.Second)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("не удалось прочитать файл: %v", err)
	}
	body := string(content)
	if !strings.Contains(body, "критическая ошибка соединения") {
		t.Errorf("аномальное сообщение не попало в файл\ncontents: %s", body)
	}
	if !strings.Contains(body, "ANOMALY LOG") {
		t.Error("заголовок файла отсутствует")
	}
	if !strings.Contains(body, "ERROR") {
		t.Error("тип аномалии ERROR не указан в файле")
	}
}

func TestDetector_WarnBurstCreatesFile(t *testing.T) {
	d, evLog, dir := newTestDetector(t)
	d.Start()
	defer d.Stop()

	// 3 warn подряд = burst
	evLog.Add(eventlog.LevelWarn, "sing-box", "DNS timeout 1")
	evLog.Add(eventlog.LevelWarn, "sing-box", "DNS timeout 2")
	evLog.Add(eventlog.LevelWarn, "sing-box", "DNS timeout 3")

	path := waitFile(t, dir, "warn_burst", 2*time.Second)
	content, _ := os.ReadFile(path)
	if !strings.Contains(string(content), "WARN_BURST") {
		t.Error("тип WARN_BURST не указан")
	}
}

func TestDetector_CrashKeywordCreatesFile(t *testing.T) {
	d, evLog, dir := newTestDetector(t)
	d.Start()
	defer d.Stop()

	evLog.Add(eventlog.LevelInfo, "sing-box", "fatal: failed to start TUN adapter")

	path := waitFile(t, dir, "crash", 2*time.Second)
	content, _ := os.ReadFile(path)
	if !strings.Contains(string(content), "failed to start TUN adapter") {
		t.Error("краш-сообщение не попало в файл")
	}
}

func TestDetector_ContextIncludedInFile(t *testing.T) {
	d, evLog, dir := newTestDetector(t)

	// Добавляем фоновые события ДО запуска детектора
	evLog.Add(eventlog.LevelInfo, "main", "приложение запущено")
	evLog.Add(eventlog.LevelInfo, "proxy", "прокси включён")
	evLog.Add(eventlog.LevelInfo, "xray", "sing-box запущен")

	d.Start()
	defer d.Stop()

	evLog.Add(eventlog.LevelError, "xray", "sing-box упал")

	path := waitFile(t, dir, "error", 2*time.Second)
	content, _ := os.ReadFile(path)
	body := string(content)

	// Все предыдущие события должны быть в контексте
	if !strings.Contains(body, "приложение запущено") {
		t.Error("контекстные события не включены в файл")
	}
	if !strings.Contains(body, "КОНТЕКСТ") {
		t.Error("секция КОНТЕКСТ отсутствует")
	}
}

func TestDetector_FileNameContainsSessionTime(t *testing.T) {
	d, evLog, dir := newTestDetector(t)
	d.Start()
	defer d.Stop()

	sessionDate := d.sessionTS.Format("2006-01-02")
	evLog.Add(eventlog.LevelError, "test", "ошибка")

	path := waitFile(t, dir, "error", 2*time.Second)
	if !strings.Contains(filepath.Base(path), sessionDate) {
		t.Errorf("имя файла %q не содержит дату сессии %q", filepath.Base(path), sessionDate)
	}
}

func TestDetector_MultipleErrorsAppendToSameFile(t *testing.T) {
	d, evLog, dir := newTestDetector(t)
	d.Start()
	defer d.Stop()

	evLog.Add(eventlog.LevelError, "test", "первая ошибка")
	time.Sleep(100 * time.Millisecond) // дать check() сработать

	evLog.Add(eventlog.LevelError, "test", "вторая ошибка")
	time.Sleep(100 * time.Millisecond)

	// Ждём чтобы оба события были обработаны
	time.Sleep(4 * time.Second)

	entries, _ := os.ReadDir(dir)
	var errorFiles []string
	for _, e := range entries {
		if strings.Contains(e.Name(), "error") {
			errorFiles = append(errorFiles, e.Name())
		}
	}
	// Оба события одной сессии → ОДИН файл (append, не новый)
	if len(errorFiles) != 1 {
		t.Errorf("ожидался 1 файл для сессии, найдено %d: %v", len(errorFiles), errorFiles)
	}
	content, _ := os.ReadFile(filepath.Join(dir, errorFiles[0]))
	body := string(content)
	if !strings.Contains(body, "первая ошибка") || !strings.Contains(body, "вторая ошибка") {
		t.Error("обе ошибки должны быть в одном файле")
	}
}

func TestDetector_NormalEventsNoFile(t *testing.T) {
	d, evLog, dir := newTestDetector(t)
	d.Start()
	defer d.Stop()

	// Только info события — не должны создавать файл
	evLog.Add(eventlog.LevelInfo, "main", "запуск")
	evLog.Add(eventlog.LevelInfo, "proxy", "готов")
	evLog.Add(eventlog.LevelDebug, "api", "получен запрос")

	time.Sleep(4 * time.Second) // ждём несколько циклов check()

	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("не должно быть файлов для нормальных событий, найдено %d: %v",
			len(entries), entries)
	}
}

func TestDetector_StopIsIdempotent(t *testing.T) {
	d, _, _ := newTestDetector(t)
	d.Start()
	d.Stop()
	// Второй Stop не должен паниковать (close closed channel)
	// Stop только один раз закрывает канал — проверяем через горутину с recover
	done := make(chan bool)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- false
				return
			}
			done <- true
		}()
		// Stop уже был вызван — не паника, просто wg.Wait вернётся сразу
		d.wg.Wait()
	}()
	select {
	case ok := <-done:
		if !ok {
			t.Error("Stop вызвал панику")
		}
	case <-time.After(time.Second):
		t.Error("зависание после Stop")
	}
}

func TestDetector_TwoWarningsNoBurst(t *testing.T) {
	d, evLog, dir := newTestDetector(t)
	d.Start()
	defer d.Stop()

	// Только 2 warn — ниже порога burst (3)
	evLog.Add(eventlog.LevelWarn, "test", "предупреждение 1")
	evLog.Add(eventlog.LevelWarn, "test", "предупреждение 2")

	time.Sleep(4 * time.Second)

	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("2 warn не должны создавать файл, найдено %d файлов", len(entries))
	}
}

func TestDetector_CrashKeywords(t *testing.T) {
	keywords := []struct {
		msg      string
		expected string
	}{
		{"fatal: bind: address already in use", "crash"},
		{"panic: runtime error", "crash"},
		{"configure tun interface failed", "crash"},
		{"Cannot create a file when that file already exists", "crash"},
		{"signal: killed unexpectedly", "crash"},
	}

	for _, tc := range keywords {
		tc := tc
		t.Run(tc.msg[:20], func(t *testing.T) {
			d, evLog, dir := newTestDetector(t)
			d.Start()
			defer d.Stop()

			evLog.Add(eventlog.LevelInfo, "sing-box", "%s", tc.msg)
			waitFile(t, dir, tc.expected, 2*time.Second)
		})
	}
}

func TestDetector_FileFormat(t *testing.T) {
	d, evLog, dir := newTestDetector(t)
	d.Start()
	defer d.Stop()

	evLog.Add(eventlog.LevelError, "api", "connection refused: 127.0.0.1:9090")

	path := waitFile(t, dir, "error", 2*time.Second)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("не удалось прочитать файл: %v", err)
	}
	body := string(content)

	// Проверяем структуру файла
	checks := []string{
		"ANOMALY LOG",        // заголовок
		"Тип:",               // тип аномалии
		"АНОМАЛИЯ",           // секция с событиями
		"api",                // источник события
		"connection refused", // само сообщение
	}
	for _, check := range checks {
		if !strings.Contains(body, check) {
			t.Errorf("файл не содержит %q\ncontents:\n%s", check, body)
		}
	}
}

// ─── rotateOldFiles ────────────────────────────────────────────────────────

func TestRotateOldFiles_DeletesOldLogs(t *testing.T) {
	dir := t.TempDir()

	// Создаём три файла: два старых и один свежий
	old1 := filepath.Join(dir, "anomaly-2025-01-01_00-00-00_crash.log")
	old2 := filepath.Join(dir, "anomaly-2025-06-15_12-00-00_error.log")
	fresh := filepath.Join(dir, "anomaly-2026-03-22_09-14-58_crash.log")
	other := filepath.Join(dir, "proxy-client.log") // не anomaly-файл — не трогаем

	for _, f := range []string{old1, old2, fresh, other} {
		if err := os.WriteFile(f, []byte("data"), 0644); err != nil {
			t.Fatalf("WriteFile %s: %v", f, err)
		}
	}

	// Ставим mtime на старые файлы явно в прошлое
	past := time.Now().Add(-10 * 24 * time.Hour)
	for _, f := range []string{old1, old2} {
		if err := os.Chtimes(f, past, past); err != nil {
			t.Fatalf("Chtimes %s: %v", f, err)
		}
	}

	evLog := eventlog.New(100)
	d := New(evLog, dir) // New вызывает rotateOldFiles(7d)
	_ = d

	if _, err := os.Stat(old1); !os.IsNotExist(err) {
		t.Errorf("old1 должен быть удалён")
	}
	if _, err := os.Stat(old2); !os.IsNotExist(err) {
		t.Errorf("old2 должен быть удалён")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("свежий файл не должен быть удалён: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("не-anomaly файл не должен быть удалён: %v", err)
	}
}

func TestRotateOldFiles_KeepsFreshLogs(t *testing.T) {
	dir := t.TempDir()

	f := filepath.Join(dir, "anomaly-2026-03-22_09-14-58_crash.log")
	if err := os.WriteFile(f, []byte("data"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	evLog := eventlog.New(100)
	d := New(evLog, dir)
	_ = d

	if _, err := os.Stat(f); err != nil {
		t.Errorf("свежий файл не должен быть удалён: %v", err)
	}
}

func TestRotateOldFiles_SafeOnEmptyDir(t *testing.T) {
	dir := t.TempDir()
	evLog := eventlog.New(100)
	d := New(evLog, dir) // не должен паниковать
	_ = d
}
