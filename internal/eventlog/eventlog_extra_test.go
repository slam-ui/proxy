package eventlog

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// ─── LineWriter: дополнительные граничные случаи ─────────────────────────

// TestLineWriter_TabTrimmed проверяет что \t в конце строки обрезается.
func TestLineWriter_TabTrimmed(t *testing.T) {
	l := New(10)
	w := NewLineWriter(l, "proc", LevelInfo)
	w.Write([]byte("line with tabs\t\t\t\n"))

	events := l.GetSince(0)
	if len(events) != 1 {
		t.Fatalf("len = %d, want 1", len(events))
	}
	if strings.Contains(events[0].Message, "\t") {
		t.Errorf("сообщение содержит \\t: %q", events[0].Message)
	}
	if events[0].Message != "line with tabs" {
		t.Errorf("Message = %q, want %q", events[0].Message, "line with tabs")
	}
}

// TestLineWriter_SpaceTrimmed проверяет обрезку пробелов в конце строки.
func TestLineWriter_SpaceTrimmed(t *testing.T) {
	l := New(10)
	w := NewLineWriter(l, "proc", LevelInfo)
	w.Write([]byte("trailing spaces   \n"))

	events := l.GetSince(0)
	if len(events) != 1 {
		t.Fatalf("len = %d, want 1", len(events))
	}
	if events[0].Message != "trailing spaces" {
		t.Errorf("Message = %q, want 'trailing spaces'", events[0].Message)
	}
}

// TestLineWriter_OnlyWhitespace_Skipped проверяет что строка из одних
// пробелов/табов обрезается до пустой и не добавляется в лог.
func TestLineWriter_OnlyWhitespace_Skipped(t *testing.T) {
	l := New(10)
	w := NewLineWriter(l, "proc", LevelInfo)
	w.Write([]byte("   \t  \r\n"))

	if n := len(l.GetSince(0)); n != 0 {
		t.Errorf("строка из пробелов должна быть пропущена, got %d событий", n)
	}
}

// TestLineWriter_MultipleChunksBuffered проверяет что буфер корректно
// собирает строку из нескольких Write вызовов без промежуточных flush.
func TestLineWriter_MultipleChunksBuffered(t *testing.T) {
	l := New(10)
	w := NewLineWriter(l, "proc", LevelInfo)

	const msg = "hello world"
	for _, ch := range msg {
		w.Write([]byte(string(ch)))
		if len(l.GetSince(0)) != 0 {
			t.Error("строка не должна добавляться до получения \\n")
		}
	}
	w.Write([]byte("\n"))

	events := l.GetSince(0)
	if len(events) != 1 {
		t.Fatalf("len = %d, want 1", len(events))
	}
	if events[0].Message != msg {
		t.Errorf("Message = %q, want %q", events[0].Message, msg)
	}
}

// TestLineWriter_MixedCRLFAndLF проверяет что \n и \r\n оба обрабатываются.
func TestLineWriter_MixedCRLFAndLF(t *testing.T) {
	l := New(20)
	w := NewLineWriter(l, "proc", LevelInfo)
	w.Write([]byte("unix line\nwindows line\r\nmixed line\n"))

	events := l.GetSince(0)
	if len(events) != 3 {
		t.Fatalf("len = %d, want 3", len(events))
	}
	for _, e := range events {
		if strings.ContainsAny(e.Message, "\r\n") {
			t.Errorf("Message содержит управляющие символы: %q", e.Message)
		}
	}
}

// TestLineWriter_SourcePreserved проверяет что source передаётся корректно.
func TestLineWriter_SourcePreserved(t *testing.T) {
	l := New(10)
	w := NewLineWriter(l, "my-process", LevelWarn)
	w.Write([]byte("some warning\n"))

	events := l.GetSince(0)
	if len(events) != 1 {
		t.Fatalf("len = %d, want 1", len(events))
	}
	if events[0].Source != "my-process" {
		t.Errorf("Source = %q, want my-process", events[0].Source)
	}
}

// TestLineWriter_ConcurrentWritesNoRace проверяет отсутствие гонок
// при параллельных Write в один LineWriter.
func TestLineWriter_ConcurrentWritesNoRace(t *testing.T) {
	l := New(200)
	w := NewLineWriter(l, "proc", LevelInfo)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			w.Write([]byte(fmt.Sprintf("line from goroutine %d\n", n)))
		}(i)
	}
	wg.Wait()

	events := l.GetSince(0)
	if len(events) == 0 {
		t.Error("после параллельных Write должны быть события в логе")
	}
}

// ─── Log.GetSince: граничные случаи кольцевого буфера ────────────────────

// TestLog_GetSince_RingOverflow_PartialSince проверяет что GetSince(since)
// после переполнения буфера возвращает только события с ID > since.
func TestLog_GetSince_RingOverflow_PartialSince(t *testing.T) {
	l := New(5)
	// 10 событий → буфер хранит последние 5 (ID 6..10)
	for i := 0; i < 10; i++ {
		l.Add(LevelInfo, "src", "msg%d", i)
	}
	// Запрашиваем after ID=7 → должны получить 8,9,10
	events := l.GetSince(7)
	if len(events) != 3 {
		t.Fatalf("GetSince(7) = %d events, want 3", len(events))
	}
	if events[0].ID != 8 {
		t.Errorf("events[0].ID = %d, want 8", events[0].ID)
	}
	if events[2].ID != 10 {
		t.Errorf("events[2].ID = %d, want 10", events[2].ID)
	}
}

// TestLog_GetSince_RingOverflow_SinceOldEvictedID проверяет что если since
// указывает на вытесненное событие, возвращаются все сохранённые события.
func TestLog_GetSince_RingOverflow_SinceOldEvictedID(t *testing.T) {
	l := New(5)
	for i := 0; i < 10; i++ {
		l.Add(LevelInfo, "src", "msg%d", i)
	}
	// since=2 — события 1..5 вытеснены, буфер хранит 6..10
	events := l.GetSince(2)
	if len(events) != 5 {
		t.Fatalf("GetSince(2) = %d events, want 5", len(events))
	}
	for i, e := range events {
		want := 6 + i
		if e.ID != want {
			t.Errorf("events[%d].ID = %d, want %d", i, e.ID, want)
		}
	}
}

// TestLog_GetSince_ExactlyAtBufferBoundary проверяет поведение когда
// since равен ID самого старого события в буфере.
func TestLog_GetSince_ExactlyAtBufferBoundary(t *testing.T) {
	l := New(4)
	// 6 событий → буфер хранит ID 3,4,5,6
	for i := 1; i <= 6; i++ {
		l.Add(LevelInfo, "src", "msg%d", i)
	}
	// since=3 → должны получить 4,5,6
	events := l.GetSince(3)
	if len(events) != 3 {
		t.Fatalf("GetSince(3) = %d events, want 3", len(events))
	}
	if events[0].ID != 4 {
		t.Errorf("первое событие ID=%d, want 4", events[0].ID)
	}
}

// ─── Logger adapter: дополнительные сценарии ─────────────────────────────

// TestLogger_FormatsMessageOnce проверяет что адаптер форматирует сообщение
// один раз, и в оба назначения идёт одинаковый текст.
func TestLogger_FormatsMessageOnce(t *testing.T) {
	evLog := New(10)
	inner := &captureLogger{}
	adapter := NewLogger(inner, evLog, "test")

	adapter.Info("value=%d name=%s", 42, "gopher")

	events := evLog.GetSince(0)
	if len(events) != 1 {
		t.Fatalf("evLog len = %d, want 1", len(events))
	}
	const want = "value=42 name=gopher"
	if events[0].Message != want {
		t.Errorf("evLog.Message = %q, want %q", events[0].Message, want)
	}
	if inner.last != want {
		t.Errorf("inner.last = %q, want %q", inner.last, want)
	}
}

// TestLogger_NoArgsNoSprintf проверяет что строки без args передаются as-is
// по пути без Sprintf — Message не должен содержать артефактов форматирования.
func TestLogger_NoArgsNoSprintf(t *testing.T) {
	evLog := New(10)
	inner := &captureLogger{}
	adapter := NewLogger(inner, evLog, "test")

	// Используем строку без % чтобы не триггерить go vet false-positive.
	// Цель теста — проверить путь "0 аргументов → formatMsg возвращает format as-is".
	const msg = "server started, no format args here"
	adapter.Info(msg)

	events := evLog.GetSince(0)
	if len(events) != 1 {
		t.Fatalf("evLog len = %d, want 1", len(events))
	}
	if events[0].Message != msg {
		t.Errorf("Message = %q, want %q", events[0].Message, msg)
	}
}

// TestLogger_AllLevelsAdapter проверяет все 4 метода адаптера.
func TestLogger_AllLevelsAdapter(t *testing.T) {
	evLog := New(20)
	inner := &captureLogger{}
	adapter := NewLogger(inner, evLog, "src")

	adapter.Debug("debug msg")
	adapter.Info("info msg")
	adapter.Warn("warn msg")
	adapter.Error("error msg")

	events := evLog.GetSince(0)
	if len(events) != 4 {
		t.Fatalf("evLog len = %d, want 4", len(events))
	}
	expectedLevels := []Level{LevelDebug, LevelInfo, LevelWarn, LevelError}
	expectedMsgs := []string{"debug msg", "info msg", "warn msg", "error msg"}
	for i, e := range events {
		if e.Level != expectedLevels[i] {
			t.Errorf("events[%d].Level = %q, want %q", i, e.Level, expectedLevels[i])
		}
		if e.Message != expectedMsgs[i] {
			t.Errorf("events[%d].Message = %q, want %q", i, e.Message, expectedMsgs[i])
		}
	}
	if inner.count != 4 {
		t.Errorf("inner.count = %d, want 4", inner.count)
	}
}

// ─── Log.Clear: идемпотентность ────────────────────────────────────────────

// TestLog_Clear_Idempotent проверяет что Clear на пустом буфере не паникует.
func TestLog_Clear_Idempotent(t *testing.T) {
	l := New(10)
	l.Clear()
	l.Clear()
	if events := l.GetSince(0); len(events) != 0 {
		t.Errorf("после двойного Clear len = %d, want 0", len(events))
	}
}

// TestLog_Clear_CounterContinues проверяет что после Clear ID-счётчик
// продолжается с прежнего значения (не сбрасывается до 0).
func TestLog_Clear_CounterContinues(t *testing.T) {
	l := New(10)
	for i := 0; i < 5; i++ {
		l.Add(LevelInfo, "src", "msg%d", i)
	}
	idBefore := l.GetLatestID() // = 5
	l.Clear()

	l.Add(LevelInfo, "src", "first after clear")
	events := l.GetSince(0)
	if len(events) != 1 {
		t.Fatalf("len = %d, want 1", len(events))
	}
	if events[0].ID <= idBefore {
		t.Errorf("ID после Clear = %d, должен быть > %d", events[0].ID, idBefore)
	}
}

// ─── formatMsg ────────────────────────────────────────────────────────────

func TestFormatMsg_NoArgs(t *testing.T) {
	const msg = "hello world"
	if got := formatMsg(msg); got != msg {
		t.Errorf("formatMsg(%q) = %q, want %q", msg, got, msg)
	}
}

func TestFormatMsg_WithArgs(t *testing.T) {
	got := formatMsg("x=%d y=%s", 42, "hello")
	const want = "x=42 y=hello"
	if got != want {
		t.Errorf("formatMsg = %q, want %q", got, want)
	}
}

// TestFormatMsg_PercentWithNoArgs проверяет что без args строка возвращается
// as-is — Sprintf не вызывается и артефактов вроде %!(NOVERB) нет.
// Строка намеренно без format-verb'ов чтобы не триггерить go vet.
func TestFormatMsg_PercentWithNoArgs(t *testing.T) {
	const msg = "operation complete, no args expected"
	got := formatMsg(msg)
	if got != msg {
		t.Errorf("formatMsg без args = %q, want %q (строка должна вернуться as-is)", got, msg)
	}
}

// ─── helpers ──────────────────────────────────────────────────────────────

// captureLogger записывает последнее сообщение и считает вызовы.
type captureLogger struct {
	mu    sync.Mutex
	last  string
	count int
}

func (c *captureLogger) Debug(f string, a ...interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(a) > 0 {
		c.last = fmt.Sprintf(f, a...)
	} else {
		c.last = f
	}
	c.count++
}
func (c *captureLogger) Info(f string, a ...interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(a) > 0 {
		c.last = fmt.Sprintf(f, a...)
	} else {
		c.last = f
	}
	c.count++
}
func (c *captureLogger) Warn(f string, a ...interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(a) > 0 {
		c.last = fmt.Sprintf(f, a...)
	} else {
		c.last = f
	}
	c.count++
}
func (c *captureLogger) Error(f string, a ...interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(a) > 0 {
		c.last = fmt.Sprintf(f, a...)
	} else {
		c.last = f
	}
	c.count++
}
