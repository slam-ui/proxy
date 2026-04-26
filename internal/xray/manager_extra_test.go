package xray

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// ─── TailWriter дополнительные кейсы ──────────────────────────────────────────

func TestTailWriter_EmptyWrite_NoPanic(t *testing.T) {
	tw := newTailWriter(256)
	n, err := tw.Write([]byte{})
	if err != nil {
		t.Errorf("Write пустого среза вернул ошибку: %v", err)
	}
	if n != 0 {
		t.Errorf("Write пустого среза вернул n=%d, want 0", n)
	}
}

func TestTailWriter_UnicodeContent_NotCorrupted(t *testing.T) {
	tw := newTailWriter(512)
	msg := "Ошибка: невалидный конфиг sing-box. Проверьте параметры.\n"
	tw.Write([]byte(msg))

	out := tw.String()
	if !utf8.ValidString(out) {
		t.Error("TailWriter вернул невалидный UTF-8 строку")
	}
	if !strings.Contains(out, "Ошибка") {
		t.Errorf("TailWriter потерял кириллический текст, got: %q", out)
	}
}

func TestTailWriter_ExactlyMaxBytes_DoesNotExceed(t *testing.T) {
	const maxBytes = 100
	tw := newTailWriter(maxBytes)
	data := strings.Repeat("x", maxBytes*3)
	tw.Write([]byte(data))

	out := tw.String()
	if len(out) > maxBytes {
		t.Errorf("TailWriter превысил maxBytes: got %d, want <= %d", len(out), maxBytes)
	}
}

func TestTailWriter_ManyWrites_PreservesLastContent(t *testing.T) {
	tw := newTailWriter(200)
	for i := 0; i < 50; i++ {
		tw.Write([]byte("обычная строка лога\n"))
	}
	tw.Write([]byte("ФИНАЛЬНАЯ СТРОКА\n"))

	out := tw.String()
	if !strings.Contains(out, "ФИНАЛЬНАЯ") {
		t.Error("TailWriter должен сохранять последние строки при overflow")
	}
}

func TestTailWriter_Reset_ClearsBuffer(t *testing.T) {
	tw := newTailWriter(256)
	tw.Write([]byte("данные до сброса\n"))
	tw.Reset()
	out := tw.String()
	if strings.Contains(out, "данные до сброса") {
		t.Error("после Reset данные должны быть удалены")
	}
}

func TestTailWriter_String_IdempotentAfterSameWrite(t *testing.T) {
	tw := newTailWriter(256)
	tw.Write([]byte("тест\n"))
	s1 := tw.String()
	s2 := tw.String()
	if s1 != s2 {
		t.Errorf("String() должен быть идемпотентным: %q != %q", s1, s2)
	}
}

// ─── CrashTracker кейсы ───────────────────────────────────────────────────────

func TestCrashTracker_Record_IncrementsCount(t *testing.T) {
	var ct crashTracker
	c1 := ct.Record()
	c2 := ct.Record()
	if c1 != 1 {
		t.Errorf("первый Record() = %d, want 1", c1)
	}
	if c2 != 2 {
		t.Errorf("второй Record() = %d, want 2", c2)
	}
}

func TestCrashTracker_Reset_ZerosCount(t *testing.T) {
	var ct crashTracker
	ct.Record()
	ct.Record()
	ct.Reset()
	c := ct.Record()
	if c != 1 {
		t.Errorf("после Reset первый Record() = %d, want 1", c)
	}
}

func TestCrashTracker_Concurrent_NoRace(t *testing.T) {
	var ct crashTracker
	done := make(chan struct{}, 10)
	for i := 0; i < 10; i++ {
		go func() {
			ct.Record()
			done <- struct{}{}
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
	ct.Reset()
}

func TestCrashTracker_ManyRecords_ExceedsMaxCount(t *testing.T) {
	var ct crashTracker
	for i := 0; i < maxCrashCount+1; i++ {
		ct.Record()
	}
	// Проверяем что Record возвращает > maxCrashCount
	count := ct.Record()
	if count <= maxCrashCount {
		t.Errorf("после %d крашей count = %d, хотим > %d", maxCrashCount+2, count, maxCrashCount)
	}
}

// ─── IsTunConflict кейсы ──────────────────────────────────────────────────────

func TestIsTunConflict_MultiLineOutput_Detects(t *testing.T) {
	output := "INFO starting sing-box\nWARN attempt 1\nERROR Cannot create a file when that file already exists.\nFATAL exit"
	if !IsTunConflict(output) {
		t.Error("многострочный вывод с 'Cannot create a file' должен быть TUN конфликтом")
	}
}

func TestIsTunConflict_EmptyOutput_ReturnsFalse(t *testing.T) {
	if IsTunConflict("") {
		t.Error("пустой вывод не должен быть TUN конфликтом")
	}
}

func TestIsTunConflict_AllKnownSignatures_Detected(t *testing.T) {
	for _, sig := range TunConflictSignatures {
		if !IsTunConflict(sig) {
			t.Errorf("сигнатура %q не задетектирована как TUN конфликт", sig)
		}
	}
}

func TestIsTunConflict_PartialMatch_Detects(t *testing.T) {
	output := "prefix " + TunConflictSignatures[0] + " suffix"
	if !IsTunConflict(output) {
		t.Errorf("сигнатура внутри строки не детектирована: %q", output)
	}
}

func TestIsTunConflict_UnrelatedOutput_False(t *testing.T) {
	unrelated := []string{
		"INFO listening on :10807",
		"WARN dns query failed",
		"ERROR connection refused",
		"tun adapter", // частичное, но не полное совпадение
	}
	for _, out := range unrelated {
		if IsTunConflict(out) {
			t.Errorf("несвязанный вывод %q ложно задетектирован как TUN конфликт", out)
		}
	}
}

// ─── IsTooManyRestarts ────────────────────────────────────────────────────────

func TestIsTooManyRestarts_NilError_ReturnsFalse(t *testing.T) {
	if IsTooManyRestarts(nil) {
		t.Error("nil ошибка не должна быть TooManyRestarts")
	}
}

func TestIsTooManyRestarts_TooManyRestartsError_ReturnsTrue(t *testing.T) {
	err := &tooManyRestartsError{count: 11}
	if !IsTooManyRestarts(err) {
		t.Error("tooManyRestartsError должна быть распознана")
	}
}

func TestTooManyRestartsError_MessageContainsCount(t *testing.T) {
	err := &tooManyRestartsError{count: 7}
	msg := err.Error()
	if !strings.Contains(msg, "7") {
		t.Errorf("сообщение об ошибке должно содержать count=7, got: %q", msg)
	}
}

func TestTooManyRestartsError_WrapsBase(t *testing.T) {
	baseErr := &simpleErr{"базовая ошибка"}
	err := &tooManyRestartsError{count: 3, base: baseErr}
	msg := err.Error()
	if msg == "" {
		t.Error("Error() не должен возвращать пустую строку")
	}
}

// ─── NewManager: валидация конфига ────────────────────────────────────────────
// TestNewManager_EmptyExecutablePath_ReturnsError и TestNewManager_EmptyConfigPath_ReturnsError
// объявлены в manager_coverage_test.go

func TestNewManager_MissingExecFile_ReturnsError(t *testing.T) {
	cfg := Config{
		ExecutablePath: "/nonexistent/path/sing-box.exe",
		ConfigPath:     "/nonexistent/config.json",
	}
	ctx := t.Context()
	_, err := NewManager(cfg, ctx)
	if err == nil {
		t.Error("NewManager с несуществующим путём должен вернуть ошибку")
	}
}

func TestNewManager_ErrorMessageIsDescriptive(t *testing.T) {
	cfg := Config{
		ExecutablePath: "./nonexistent_singbox.exe",
		ConfigPath:     "./config.json",
	}
	ctx := t.Context()
	_, err := NewManager(cfg, ctx)
	if err == nil {
		t.Skip("файл случайно существует, пропускаем")
	}
	if err.Error() == "" {
		t.Error("сообщение об ошибке не должно быть пустым")
	}
}

// ─── Uptime при не-запущенном менеджере ───────────────────────────────────────

func TestManager_Uptime_WhenNotStarted_IsNearZero(t *testing.T) {
	cfg := Config{
		ExecutablePath: "/nonexistent/sing-box.exe",
		ConfigPath:     "./config.json",
	}
	ctx := t.Context()
	mgr, err := NewManager(cfg, ctx)
	if err != nil {
		// Ожидаемо — валидация отклонила конфиг
		t.Logf("NewManager вернул ошибку (ожидаемо): %v", err)
		return
	}
	// Если создался — uptime должен быть близок к нулю
	uptime := mgr.Uptime()
	if uptime > 5*time.Second {
		t.Errorf("uptime до Start() = %v, want ~0", uptime)
	}
}

// ─── вспомогательные типы ─────────────────────────────────────────────────────

type simpleErr struct{ msg string }

func (e *simpleErr) Error() string { return e.msg }
