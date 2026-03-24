package eventlog

import (
	"testing"
)

// ── ZeroSize buffer ────────────────────────────────────────────────────────

// BUG-РИСК: New(0) не должен паниковать при Add или GetSince.
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

// ── GetSince с будущим ID ─────────────────────────────────────────────────

// BUG-РИСК: GetSince с очень большим afterID должен вернуть пустой non-nil слайс.
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

// ── Add: пустые поля ─────────────────────────────────────────────────────

// BUG-РИСК: Add с пустым source и format без args не должен ронять.
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

// ── Add: формат с % без args не должен паниковать ────────────────────────

// BUG-РИСК: Add("100% done") без args вызывает fmt.Sprintf только если len(args)>0,
// поэтому "%" не обрабатывается как глагол — это ОК. Проверяем что не паникует.
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
	// Add без args не вызывает Sprintf, поэтому "%%" остаётся как есть
	if events[0].Message != "done 100%%" {
		t.Errorf("Message = %q, want 'done 100%%%%'", events[0].Message)
	}
}

// ── GetSince: возвращает хронологический порядок после Clear ──────────────

// BUG-РИСК: после Clear + Add события должны быть в хронологическом порядке.
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

// ── LineWriter: Flush сбрасывает буфер при Close ──────────────────────────

// BUG-РИСК: незавершённая строка без финального \n должна быть добавлена в лог
// при вызове Flush (если он есть) или оставаться в буфере до следующего Write.
// Проверяем что данные не теряются при большом объёме.
func TestLineWriter_NoDataLost_ManySmallWrites(t *testing.T) {
	l := New(1000)
	w := NewLineWriter(l, "proc", LevelInfo)

	// Пишем по одному байту
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

// ── RingBuffer: GetSince после многократных overflow — правильный срез ────

// BUG-РИСК: GetSince(lastID) после многочисленных вытеснений должен
// вернуть ровно те события, которые появились после lastID.
func TestLog_GetSince_AfterOverflow_CorrectSlice(t *testing.T) {
	l := New(5)

	// Добавляем 10 событий — буфер переполняется дважды
	for i := 0; i < 10; i++ {
		l.Add(LevelInfo, "src", "msg-%d", i)
	}
	midID := l.GetLatestID() // = 10

	// Добавляем ещё 3
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
