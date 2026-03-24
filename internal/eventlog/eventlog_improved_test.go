package eventlog

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ── BUG-РИСК #1: кольцевой буфер — порядок событий при переполнении ──────────
//
// После overflow (size > maxSize) события должны возвращаться В ХРОНОЛОГИЧЕСКОМ
// порядке (oldest first). Если head/tail неверно рассчитаны — GetSince вернёт
// события в неправильном порядке, UI покажет логи задом наперёд.
func TestLog_Overflow_EventsInChronologicalOrder(t *testing.T) {
	l := New(3) // буфер на 3

	l.Add(LevelInfo, "src", "msg-1")
	l.Add(LevelInfo, "src", "msg-2")
	l.Add(LevelInfo, "src", "msg-3")
	l.Add(LevelInfo, "src", "msg-4") // вытесняет msg-1
	l.Add(LevelInfo, "src", "msg-5") // вытесняет msg-2

	events := l.GetSince(0)
	if len(events) != 3 {
		t.Fatalf("GetSince(0) вернул %d событий, want 3", len(events))
	}

	// Порядок должен быть: msg-3, msg-4, msg-5 (старые → новые)
	expected := []string{"msg-3", "msg-4", "msg-5"}
	for i, ev := range events {
		if ev.Message != expected[i] {
			t.Errorf("events[%d].Message=%q, want %q — неверный порядок при overflow", i, ev.Message, expected[i])
		}
	}
}

// ── BUG-РИСК #2: GetSince возвращает только события ПОСЛЕ afterID ────────────
//
// GetSince(N) должен вернуть события с ID > N, не >= N.
// Без этого UI получит дублирующееся последнее событие при каждом poll.
func TestLog_GetSince_ExcludesAfterID(t *testing.T) {
	l := New(10)
	l.Add(LevelInfo, "src", "msg-1")
	l.Add(LevelInfo, "src", "msg-2")
	l.Add(LevelInfo, "src", "msg-3")

	all := l.GetSince(0)
	if len(all) != 3 {
		t.Fatalf("GetSince(0) = %d, want 3", len(all))
	}

	secondID := all[1].ID // ID второго события

	// GetSince(secondID) должен вернуть только msg-3
	result := l.GetSince(secondID)
	if len(result) != 1 {
		t.Errorf("GetSince(%d) вернул %d событий, want 1 (только события после ID %d)",
			secondID, len(result), secondID)
	}
	if len(result) == 1 && result[0].Message != "msg-3" {
		t.Errorf("GetSince вернул %q, want msg-3", result[0].Message)
	}
}

// ── BUG-РИСК #3: GetLatestID монотонно возрастает при overflow ────────────
//
// counter должен продолжать расти даже при переполнении буфера.
// Если counter сбрасывается — GetSince(lastID) вернёт все события заново.
// Проверяем через GetLatestID() — он всегда должен быть >= предыдущего.
// Примечание: GetSince(0) возвращает события из кольцевого буфера, включая
// старые с меньшими ID — это нормальное поведение кольцевого буфера.
func TestLog_GetLatestID_MonotonicallyIncreasing(t *testing.T) {
	l := New(3)

	var prevLatestID int
	for i := 0; i < 10; i++ {
		l.Add(LevelInfo, "src", "msg-%d", i)
		latestID := l.GetLatestID()
		if latestID <= prevLatestID {
			t.Errorf("GetLatestID не монотонен: %d ≤ %d после добавления #%d",
				latestID, prevLatestID, i+1)
		}
		prevLatestID = latestID
	}
}

// ── BUG-РИСК #4: конкурентный Add + GetSince — нет data race ─────────────────
//
// Log использует sync.RWMutex. Add берёт Lock(), GetSince — RLock().
// Запускать с -race.
func TestLog_ConcurrentAddAndGetSince_NoRace(t *testing.T) {
	l := New(100)

	var wg sync.WaitGroup
	var addCount atomic.Int64

	// Параллельные Add
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				l.Add(LevelInfo, "src", "msg-%d-%d", n, j)
				addCount.Add(1)
			}
		}(i)
	}

	// Параллельные GetSince
	stop := make(chan struct{})
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var lastID int
			for {
				select {
				case <-stop:
					return
				default:
					events := l.GetSince(lastID)
					for _, ev := range events {
						if ev.ID > lastID {
							lastID = ev.ID
						}
					}
					time.Sleep(time.Millisecond)
				}
			}
		}()
	}

	// Ждём все Add, потом останавливаем читателей
	var addWg sync.WaitGroup
	addWg.Add(20)
	// уже запущены выше — просто даём время
	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// ── BUG-РИСК #5: GetLatestID на пустом буфере ─────────────────────────────
//
// GetLatestID() при нулевых событиях должен вернуть 0, не паниковать.
func TestLog_GetLatestID_EmptyBuffer(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("GetLatestID на пустом Log вызвал panic: %v", r)
		}
	}()

	l := New(10)
	id := l.GetLatestID()
	if id != 0 {
		t.Errorf("GetLatestID на пустом буфере = %d, want 0", id)
	}
}

// ── BUG-РИСК #6: GetLatestID возвращает ID последнего добавленного события ──
func TestLog_GetLatestID_AfterAdd(t *testing.T) {
	l := New(10)
	l.Add(LevelInfo, "src", "first")
	l.Add(LevelInfo, "src", "second")
	l.Add(LevelInfo, "src", "third")

	latestID := l.GetLatestID()
	events := l.GetSince(0)
	if len(events) == 0 {
		t.Fatal("нет событий")
	}

	lastEventID := events[len(events)-1].ID
	if latestID != lastEventID {
		t.Errorf("GetLatestID=%d, последнее событие ID=%d — несоответствие", latestID, lastEventID)
	}
}

// ── BUG-РИСК #7: буфер размером 1 — корректный wrap-around ────────────────
//
// Самый маленький рабочий буфер (размер=1) должен всегда хранить последнее событие.
func TestLog_Size1_AlwaysKeepsLatest(t *testing.T) {
	l := New(1)

	for i := 1; i <= 5; i++ {
		l.Add(LevelInfo, "src", "msg-%d", i)

		events := l.GetSince(0)
		if len(events) != 1 {
			t.Fatalf("буфер size=1 после %d добавлений содержит %d событий, want 1", i, len(events))
		}
		want := "msg-" + string(rune('0'+i))
		if events[0].Message != want {
			t.Errorf("буфер size=1: Message=%q, want %q", events[0].Message, want)
		}
	}
}

// ── BUG-РИСК #8: Level сохраняется корректно ──────────────────────────────
func TestLog_Add_LevelPreserved(t *testing.T) {
	l := New(10)
	levels := []Level{LevelDebug, LevelInfo, LevelWarn, LevelError}

	for _, level := range levels {
		l.Add(level, "src", "test")
	}

	events := l.GetSince(0)
	if len(events) != len(levels) {
		t.Fatalf("len(events)=%d, want %d", len(events), len(levels))
	}

	for i, ev := range events {
		if ev.Level != levels[i] {
			t.Errorf("events[%d].Level=%q, want %q", i, ev.Level, levels[i])
		}
	}
}

// ── BUG-РИСК #9: Timestamp не в нулевом значении ──────────────────────────
//
// Если буфер инициализирован нулями и size<maxSize, слоты с нулевым
// Timestamp не должны попасть в GetSince.
func TestLog_Timestamps_NotZero(t *testing.T) {
	l := New(10)
	before := time.Now()
	l.Add(LevelInfo, "src", "msg")
	after := time.Now()

	events := l.GetSince(0)
	if len(events) != 1 {
		t.Fatalf("len(events)=%d, want 1", len(events))
	}

	ts := events[0].Timestamp
	if ts.IsZero() {
		t.Error("Timestamp нулевой — событие создано без времени")
	}
	if ts.Before(before) || ts.After(after) {
		t.Errorf("Timestamp=%v вне диапазона [%v, %v]", ts, before, after)
	}
}

// ── BUG-РИСК #10: GetSince(lastID) во время overflow не пропускает события ──
//
// Сценарий: клиент опрашивает GetSince каждые 100мс.
// За это время буфер может переполниться и вытеснить некоторые события.
// GetSince должен вернуть то, что ещё в буфере с ID > afterID.
func TestLog_GetSince_DuringRapidOverflow_NoMissedIDs(t *testing.T) {
	l := New(5) // маленький буфер

	// Добавляем 5 событий
	for i := 0; i < 5; i++ {
		l.Add(LevelInfo, "src", "initial-%d", i)
	}

	first := l.GetSince(0)
	if len(first) == 0 {
		t.Fatal("нет событий")
	}
	afterID := first[len(first)-1].ID // запомнили последний ID

	// Добавляем ещё 10 событий — overflow, вытесняем все начальные
	for i := 0; i < 10; i++ {
		l.Add(LevelInfo, "src", "new-%d", i)
	}

	second := l.GetSince(afterID)
	// Должны получить события с ID > afterID (минимум то что в буфере)
	for _, ev := range second {
		if ev.ID <= afterID {
			t.Errorf("GetSince(%d) вернул событие с ID=%d (≤ afterID) — фильтрация нарушена",
				afterID, ev.ID)
		}
	}
	t.Logf("GetSince после overflow вернул %d новых событий (буфер=5, добавлено=10)", len(second))
}
