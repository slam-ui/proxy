package eventlog

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// ─── Базовые операции ──────────────────────────────────────────────────────

func TestLog_Add_And_GetSince_Zero(t *testing.T) {
	l := New(100)
	l.Add(LevelInfo, "test", "hello %s", "world")
	l.Add(LevelWarn, "test", "second")

	events := l.GetSince(0)
	if len(events) != 2 {
		t.Fatalf("GetSince(0) = %d events, want 2", len(events))
	}
	if events[0].Message != "hello world" {
		t.Errorf("events[0].Message = %q", events[0].Message)
	}
	if events[1].Level != LevelWarn {
		t.Errorf("events[1].Level = %q, want warn", events[1].Level)
	}
}

func TestLog_GetSince_FiltersOld(t *testing.T) {
	l := New(100)
	l.Add(LevelInfo, "src", "msg1")
	l.Add(LevelInfo, "src", "msg2")
	l.Add(LevelInfo, "src", "msg3")

	events := l.GetSince(2)
	if len(events) != 1 {
		t.Fatalf("GetSince(2) = %d events, want 1", len(events))
	}
	if events[0].Message != "msg3" {
		t.Errorf("events[0].Message = %q, want msg3", events[0].Message)
	}
}

func TestLog_GetSince_ExactBoundary(t *testing.T) {
	l := New(100)
	l.Add(LevelInfo, "src", "a") // ID=1
	l.Add(LevelInfo, "src", "b") // ID=2

	if events := l.GetSince(1); len(events) != 1 || events[0].Message != "b" {
		t.Errorf("GetSince(1) = %v, want [b]", events)
	}
	if len(l.GetSince(2)) != 0 {
		t.Error("GetSince(latestID) должен вернуть пустой слайс")
	}
	if len(l.GetSince(999)) != 0 {
		t.Error("GetSince(будущий ID) должен вернуть пустой слайс")
	}
}

func TestLog_GetSince_ReturnsChronologicalOrder(t *testing.T) {
	l := New(100)
	for i := 0; i < 10; i++ {
		l.Add(LevelInfo, "src", "msg%d", i)
	}
	events := l.GetSince(0)
	for i := 1; i < len(events); i++ {
		if events[i].ID <= events[i-1].ID {
			t.Errorf("нарушен порядок: events[%d].ID=%d <= events[%d].ID=%d",
				i, events[i].ID, i-1, events[i-1].ID)
		}
	}
}

func TestLog_IDs_AreMonotonicallyIncreasing(t *testing.T) {
	l := New(5)
	for i := 0; i < 20; i++ {
		l.Add(LevelInfo, "src", "msg%d", i)
	}
	events := l.GetSince(0)
	for i := 1; i < len(events); i++ {
		if events[i].ID <= events[i-1].ID {
			t.Errorf("ID не монотонный на позиции %d", i)
		}
	}
}

func TestLog_TimestampIsSet(t *testing.T) {
	before := time.Now()
	l := New(5)
	l.Add(LevelInfo, "src", "msg")
	after := time.Now()

	e := l.GetSince(0)[0]
	if e.Timestamp.Before(before) || e.Timestamp.After(after) {
		t.Errorf("Timestamp %v вне диапазона [%v, %v]", e.Timestamp, before, after)
	}
}

func TestLog_Empty_GetSince_ReturnsNonNil(t *testing.T) {
	l := New(10)
	events := l.GetSince(0)
	if events == nil {
		t.Error("GetSince на пустом логе не должен возвращать nil")
	}
	if len(events) != 0 {
		t.Errorf("GetSince на пустом логе вернул %d событий", len(events))
	}
}

// ─── Кольцевой буфер ───────────────────────────────────────────────────────

func TestLog_RingBuffer_EvictsOldestOnOverflow(t *testing.T) {
	l := New(3)
	l.Add(LevelInfo, "src", "first")
	l.Add(LevelInfo, "src", "second")
	l.Add(LevelInfo, "src", "third")
	l.Add(LevelInfo, "src", "fourth") // вытесняет "first"

	events := l.GetSince(0)
	if len(events) != 3 {
		t.Fatalf("len = %d, want 3", len(events))
	}
	for _, m := range events {
		if m.Message == "first" {
			t.Error("«first» должно быть вытеснено")
		}
	}
	if events[2].Message != "fourth" {
		t.Errorf("последнее событие = %q, want fourth", events[2].Message)
	}
}

func TestLog_RingBuffer_CapacityOne(t *testing.T) {
	l := New(1)
	l.Add(LevelInfo, "src", "a")
	l.Add(LevelInfo, "src", "b")
	l.Add(LevelInfo, "src", "c")

	events := l.GetSince(0)
	if len(events) != 1 {
		t.Fatalf("len = %d, want 1", len(events))
	}
	if events[0].Message != "c" {
		t.Errorf("events[0].Message = %q, want c", events[0].Message)
	}
}

func TestLog_RingBuffer_SizeNeverExceedsMax(t *testing.T) {
	max := 7
	l := New(max)
	for i := 0; i < max*3; i++ {
		l.Add(LevelInfo, "src", "msg%d", i)
		if n := len(l.GetSince(0)); n > max {
			t.Fatalf("после %d добавлений len=%d превышает max=%d", i+1, n, max)
		}
	}
}

func TestLog_RingBuffer_OrderAfterManyOverflows(t *testing.T) {
	l := New(5)
	for i := 0; i < 100; i++ {
		l.Add(LevelInfo, "src", "msg%d", i)
	}
	events := l.GetSince(0)
	for i := 1; i < len(events); i++ {
		if events[i].ID <= events[i-1].ID {
			t.Errorf("нарушен порядок после overflow на позиции %d", i)
		}
	}
	if events[len(events)-1].Message != "msg99" {
		t.Errorf("последнее = %q, want msg99", events[len(events)-1].Message)
	}
}

func TestLog_GetLatestID(t *testing.T) {
	l := New(10)
	if l.GetLatestID() != 0 {
		t.Error("GetLatestID на пустом буфере должен возвращать 0")
	}
	l.Add(LevelInfo, "src", "a")
	l.Add(LevelInfo, "src", "b")
	if l.GetLatestID() != 2 {
		t.Errorf("GetLatestID = %d, want 2", l.GetLatestID())
	}
}

func TestLog_GetLatestID_AfterOverflow(t *testing.T) {
	l := New(3)
	for i := 1; i <= 10; i++ {
		l.Add(LevelInfo, "src", "msg")
	}
	if l.GetLatestID() != 10 {
		t.Errorf("GetLatestID = %d, want 10", l.GetLatestID())
	}
}

func TestLog_Clear(t *testing.T) {
	l := New(10)
	l.Add(LevelInfo, "src", "a")
	l.Add(LevelInfo, "src", "b")
	l.Clear()

	if events := l.GetSince(0); len(events) != 0 {
		t.Errorf("после Clear() len = %d, want 0", len(events))
	}

	// Counter НЕ сбрасывается — polling-клиенты не получат старые события.
	idBefore := l.GetLatestID()
	l.Add(LevelInfo, "src", "new")
	events := l.GetSince(0)
	if len(events) != 1 {
		t.Fatalf("после Clear()+Add len = %d, want 1", len(events))
	}
	if events[0].ID <= idBefore {
		t.Errorf("ID после Clear() должен быть > %d, got %d", idBefore, events[0].ID)
	}
	if events[0].Message != "new" {
		t.Errorf("Message = %q, want new", events[0].Message)
	}
}

func TestLog_Clear_GetSince_ReturnsOnlyNew(t *testing.T) {
	l := New(20)
	for i := 0; i < 5; i++ {
		l.Add(LevelInfo, "src", "old-%d", i)
	}
	lastBefore := l.GetLatestID()
	l.Clear()
	for i := 0; i < 3; i++ {
		l.Add(LevelInfo, "src", "new-%d", i)
	}
	events := l.GetSince(lastBefore)
	if len(events) != 3 {
		t.Fatalf("len = %d, want 3", len(events))
	}
	for _, e := range events {
		if !strings.HasPrefix(e.Message, "new-") {
			t.Errorf("ожидались только new-* события, got %q", e.Message)
		}
	}
}

// ─── Уровни и форматирование ───────────────────────────────────────────────

func TestLog_Add_Levels(t *testing.T) {
	l := New(10)
	l.Add(LevelDebug, "src", "debug")
	l.Add(LevelInfo, "src", "info")
	l.Add(LevelWarn, "src", "warn")
	l.Add(LevelError, "src", "error")

	events := l.GetSince(0)
	if len(events) != 4 {
		t.Fatalf("len = %d, want 4", len(events))
	}
	expected := []Level{LevelDebug, LevelInfo, LevelWarn, LevelError}
	for i, ev := range events {
		if ev.Level != expected[i] {
			t.Errorf("events[%d].Level = %q, want %q", i, ev.Level, expected[i])
		}
	}
}

func TestLog_Add_FormatArgs(t *testing.T) {
	tests := []struct {
		format string
		args   []interface{}
		want   string
	}{
		{"host=%s port=%d", []interface{}{"example.com", 443}, "host=example.com port=443"},
		{"no args", nil, "no args"},
		{"err: %v", []interface{}{fmt.Errorf("timeout")}, "err: timeout"},
		{"%.2f", []interface{}{3.14159}, "3.14"},
	}
	for _, tc := range tests {
		l := New(5)
		l.Add(LevelInfo, "src", tc.format, tc.args...)
		if got := l.GetSince(0)[0].Message; got != tc.want {
			t.Errorf("format=%q: got %q, want %q", tc.format, got, tc.want)
		}
	}
}

func TestLog_Add_NoArgs_NoSprintf(t *testing.T) {
	l := New(5)
	l.Add(LevelInfo, "src", "100 percent done")
	if msg := l.GetSince(0)[0].Message; msg != "100 percent done" {
		t.Errorf("Message = %q", msg)
	}
}

func TestLog_Add_SourcePreserved(t *testing.T) {
	sources := []string{"sing-box", "main", "api", "proxy"}
	l := New(10)
	for _, src := range sources {
		l.Add(LevelInfo, src, "msg")
	}
	for i, e := range l.GetSince(0) {
		if e.Source != sources[i] {
			t.Errorf("events[%d].Source = %q, want %q", i, e.Source, sources[i])
		}
	}
}

// ─── LineWriter ────────────────────────────────────────────────────────────

func TestLineWriter_SplitsOnNewlines(t *testing.T) {
	l := New(20)
	w := NewLineWriter(l, "proc", LevelInfo)
	w.Write([]byte("line one\nline two\nline three\n"))

	events := l.GetSince(0)
	if len(events) != 3 {
		t.Fatalf("ожидали 3 события, got %d", len(events))
	}
	if events[0].Message != "line one" || events[2].Message != "line three" {
		t.Errorf("неверные сообщения: %v", events)
	}
}

func TestLineWriter_NoNewlineAtEnd_FlushesOnNext(t *testing.T) {
	l := New(20)
	w := NewLineWriter(l, "proc", LevelInfo)

	w.Write([]byte("partial"))
	if len(l.GetSince(0)) != 0 {
		t.Error("неполная строка не должна добавляться в лог")
	}

	w.Write([]byte(" line\nnext\n"))
	events := l.GetSince(0)
	if len(events) != 2 {
		t.Fatalf("len = %d, want 2", len(events))
	}
	if events[0].Message != "partial line" {
		t.Errorf("events[0].Message = %q", events[0].Message)
	}
}

func TestLineWriter_EmptyLines_Skipped(t *testing.T) {
	l := New(20)
	w := NewLineWriter(l, "proc", LevelInfo)
	w.Write([]byte("\n\nhello\n\n"))
	events := l.GetSince(0)
	if len(events) != 1 {
		t.Errorf("len = %d, want 1 (только 'hello')", len(events))
	}
	for _, e := range events {
		if strings.TrimSpace(e.Message) == "" {
			t.Error("пустая строка попала в лог")
		}
	}
}

func TestLineWriter_WindowsCRLF(t *testing.T) {
	l := New(20)
	w := NewLineWriter(l, "proc", LevelInfo)
	w.Write([]byte("line one\r\nline two\r\n"))

	events := l.GetSince(0)
	if len(events) != 2 {
		t.Fatalf("len = %d, want 2", len(events))
	}
	if strings.Contains(events[0].Message, "\r") {
		t.Errorf("\\r не trimmed: %q", events[0].Message)
	}
}

func TestLineWriter_LargeWrite(t *testing.T) {
	l := New(1000)
	w := NewLineWriter(l, "proc", LevelInfo)
	var sb strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&sb, "line %d\n", i)
	}
	w.Write([]byte(sb.String()))
	if n := len(l.GetSince(0)); n != 100 {
		t.Errorf("len = %d, want 100", n)
	}
}

func TestLineWriter_LevelPreserved(t *testing.T) {
	l := New(10)
	w := NewLineWriter(l, "proc", LevelError)
	w.Write([]byte("error line\n"))
	if events := l.GetSince(0); events[0].Level != LevelError {
		t.Errorf("Level = %q, want error", events[0].Level)
	}
}

// ─── Logger adapter ────────────────────────────────────────────────────────

func TestLogger_WritesToBothInnerAndEvLog(t *testing.T) {
	evLog := New(20)
	inner := &testInnerLogger{}
	adapter := NewLogger(inner, evLog, "adapter")

	adapter.Debug("debug %d", 1)
	adapter.Info("info %s", "hello")
	adapter.Warn("warn")
	adapter.Error("error %v", fmt.Errorf("oops"))

	events := evLog.GetSince(0)
	if len(events) != 4 {
		t.Fatalf("evLog len = %d, want 4", len(events))
	}
	if events[0].Level != LevelDebug {
		t.Errorf("events[0].Level = %q, want debug", events[0].Level)
	}
	if events[1].Message != "info hello" {
		t.Errorf("events[1].Message = %q, want 'info hello'", events[1].Message)
	}
	if events[3].Message != "error oops" {
		t.Errorf("events[3].Message = %q", events[3].Message)
	}
	if inner.count != 4 {
		t.Errorf("inner.count = %d, want 4", inner.count)
	}
	for _, e := range events {
		if e.Source != "adapter" {
			t.Errorf("Source = %q, want adapter", e.Source)
		}
	}
}

type testInnerLogger struct {
	mu    sync.Mutex
	count int
}

func (l *testInnerLogger) Debug(f string, a ...interface{}) { l.mu.Lock(); l.count++; l.mu.Unlock() }
func (l *testInnerLogger) Info(f string, a ...interface{})  { l.mu.Lock(); l.count++; l.mu.Unlock() }
func (l *testInnerLogger) Warn(f string, a ...interface{})  { l.mu.Lock(); l.count++; l.mu.Unlock() }
func (l *testInnerLogger) Error(f string, a ...interface{}) { l.mu.Lock(); l.count++; l.mu.Unlock() }

// ─── Concurrent safety ─────────────────────────────────────────────────────

func TestLog_ConcurrentAddAndGet(t *testing.T) {
	l := New(50)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			l.Add(LevelInfo, "src", "msg-%d", n)
		}(i)
	}
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.GetSince(0)
			l.GetLatestID()
		}()
	}
	wg.Wait()
}

func TestLog_ConcurrentClearAndAdd(t *testing.T) {
	l := New(20)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				l.Add(LevelInfo, "src", "msg-%d-%d", n, j)
			}
		}(i)
	}
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.Clear()
		}()
	}
	wg.Wait()
	events := l.GetSince(0)
	for j := 1; j < len(events); j++ {
		if events[j].ID <= events[j-1].ID {
			t.Errorf("нарушен порядок ID после concurrent Clear: %d <= %d",
				events[j].ID, events[j-1].ID)
		}
	}
}
