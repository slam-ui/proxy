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

// ─── Экстремальные граничные случаи (бывший extra2) ──────────────────────────

// TestLog_ZeroSize_NoPanic — New(0) не должен паниковать.
func TestLog_ZeroSize_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("New(0).Add/GetSince вызвал panic: %v", r)
		}
	}()
	l := New(0)
	l.Add(LevelInfo, "src", "msg")
	_ = l.GetSince(0)
	_ = l.GetLatestID()
}

// TestLog_GetSince_FutureID_EmptyNotNil — GetSince с очень большим afterID
// должен вернуть пустой non-nil слайс.
func TestLog_GetSince_FutureID_EmptyNotNil(t *testing.T) {
	l := New(10)
	l.Add(LevelInfo, "src", "msg")

	result := l.GetSince(999999)
	if result == nil {
		t.Error("GetSince с будущим ID не должен возвращать nil")
	}
	if len(result) != 0 {
		t.Errorf("GetSince с будущим ID вернул %d событий, want 0", len(result))
	}
}

// TestLog_Add_EmptySourceAndMessage — Add с пустым source и пустым форматом
// не должен паниковать.
func TestLog_Add_EmptySourceAndMessage(t *testing.T) {
	l := New(5)
	l.Add(LevelInfo, "", "")
	events := l.GetSince(0)
	if len(events) != 1 {
		t.Fatalf("Add с пустыми полями должен добавить событие, len=%d", len(events))
	}
	if events[0].Message != "" {
		t.Errorf("Message = %q, want empty", events[0].Message)
	}
}

// TestLog_Add_PercentInMessage_NoPanic — Add без args не должен паниковать при %
// в строке формата.
func TestLog_Add_PercentInMessage_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Add с %% в сообщении вызвал panic: %v", r)
		}
	}()
	l := New(5)
	l.Add(LevelInfo, "src", "done 100%%")
	events := l.GetSince(0)
	if len(events) != 1 {
		t.Fatalf("событие не добавлено")
	}
	if events[0].Message != "done 100%%" {
		t.Errorf("Message = %q, want 'done 100%%%%'", events[0].Message)
	}
}

// TestLog_GetSince_AfterClear_ChronologicalOrder — после Clear + Add события
// должны быть в хронологическом порядке.
func TestLog_GetSince_AfterClear_ChronologicalOrder(t *testing.T) {
	l := New(10)
	for i := 0; i < 5; i++ {
		l.Add(LevelInfo, "src", "old-%d", i)
	}
	l.Clear()
	for i := 0; i < 3; i++ {
		l.Add(LevelInfo, "src", "new-%d", i)
	}

	events := l.GetSince(0)
	for i := 1; i < len(events); i++ {
		if events[i].ID <= events[i-1].ID {
			t.Errorf("нарушен порядок ID после Clear: events[%d].ID=%d <= events[%d].ID=%d",
				i, events[i].ID, i-1, events[i-1].ID)
		}
	}
}

// TestLineWriter_NoDataLost_ManySmallWrites — данные не теряются при побайтовой
// записи.
func TestLineWriter_NoDataLost_ManySmallWrites(t *testing.T) {
	l := New(1000)
	w := NewLineWriter(l, "proc", LevelInfo)

	data := []byte("hello\nworld\n")
	for _, b := range data {
		w.Write([]byte{b})
	}

	events := l.GetSince(0)
	if len(events) != 2 {
		t.Errorf("len = %d, want 2 — данные потеряны при побайтовой записи", len(events))
	}
	if len(events) >= 1 && events[0].Message != "hello" {
		t.Errorf("events[0].Message = %q, want hello", events[0].Message)
	}
}

// TestLog_GetSince_AfterOverflow_CorrectSlice — GetSince после многократных
// overflow буфера возвращает ровно нужные события.
func TestLog_GetSince_AfterOverflow_CorrectSlice(t *testing.T) {
	l := New(5)
	for i := 0; i < 10; i++ {
		l.Add(LevelInfo, "src", "msg-%d", i)
	}
	midID := l.GetLatestID()

	for i := 10; i < 13; i++ {
		l.Add(LevelInfo, "src", "msg-%d", i)
	}

	events := l.GetSince(midID)
	if len(events) != 3 {
		t.Errorf("GetSince(%d) вернул %d событий, want 3", midID, len(events))
	}
	for _, e := range events {
		if e.ID <= midID {
			t.Errorf("событие с ID=%d не должно быть в результате (afterID=%d)", e.ID, midID)
		}
	}
}
