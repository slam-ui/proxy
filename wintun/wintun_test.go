//go:build windows

package wintun_test

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"proxyclient/internal/wintun"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// inTempDir выполняет fn в изолированной временной директории.
// НЕ использует t.Parallel(): os.Chdir меняет CWD для всего процесса
// на Windows, поэтому параллельные тесты с Chdir создают race condition.
func inTempDir(t *testing.T, fn func()) {
	t.Helper()
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(old) }()
	fn()
}

func writeGapFile(t *testing.T, d time.Duration) {
	t.Helper()
	_ = os.WriteFile("wintun_gap_ns",
		[]byte(strconv.FormatInt(int64(d), 10)), 0644)
}

func writeStopFile(t *testing.T, at time.Time) {
	t.Helper()
	_ = os.WriteFile(wintun.StopFile,
		[]byte(strconv.FormatInt(at.UnixNano(), 10)), 0644)
}

// ── RecordStop ────────────────────────────────────────────────────────────────

func TestRecordStop_CreatesFile(t *testing.T) {
	inTempDir(t, func() {
		wintun.RecordStop()

		data, err := os.ReadFile(wintun.StopFile)
		if err != nil {
			t.Fatalf("StopFile не создан: %v", err)
		}
		ns, err := strconv.ParseInt(string(data), 10, 64)
		if err != nil {
			t.Fatalf("невалидный timestamp: %q", data)
		}
		elapsed := time.Since(time.Unix(0, ns))
		if elapsed < 0 || elapsed > 2*time.Second {
			t.Errorf("timestamp далеко от now: elapsed=%v", elapsed)
		}
	})
}

func TestRecordStop_OverwritesPrevious(t *testing.T) {
	inTempDir(t, func() {
		wintun.RecordStop()
		first, _ := os.ReadFile(wintun.StopFile)

		time.Sleep(5 * time.Millisecond)
		wintun.RecordStop()
		second, _ := os.ReadFile(wintun.StopFile)

		if string(first) == string(second) {
			t.Error("второй RecordStop должен обновить timestamp")
		}
	})
}

// ── ReadAdaptiveGap ───────────────────────────────────────────────────────────

func TestReadAdaptiveGap_DefaultWhenNoFile(t *testing.T) {
	inTempDir(t, func() {
		gap := wintun.ReadAdaptiveGap()
		if gap != 60*time.Second {
			t.Errorf("got %v, want 60s (minGapBase)", gap)
		}
	})
}

func TestReadAdaptiveGap_DefaultWhenCorruptFile(t *testing.T) {
	inTempDir(t, func() {
		_ = os.WriteFile("wintun_gap_ns", []byte("not-a-number"), 0644)
		gap := wintun.ReadAdaptiveGap()
		if gap != 60*time.Second {
			t.Errorf("got %v, want 60s for corrupt file", gap)
		}
	})
}

func TestReadAdaptiveGap_CapsAtMaxGap(t *testing.T) {
	inTempDir(t, func() {
		// 10 минут > minGapMax (3 минуты) → должно вернуть 3m
		writeGapFile(t, 10*time.Minute)
		gap := wintun.ReadAdaptiveGap()
		if gap != 3*time.Minute {
			t.Errorf("got %v, want 3m (minGapMax cap)", gap)
		}
	})
}

func TestReadAdaptiveGap_FloorsAtMinGapBase(t *testing.T) {
	inTempDir(t, func() {
		// 1 секунда < minGapBase (60 секунд) → должно вернуть 60s
		writeGapFile(t, 1*time.Second)
		gap := wintun.ReadAdaptiveGap()
		if gap != 60*time.Second {
			t.Errorf("got %v, want 60s (minGapBase floor)", gap)
		}
	})
}

func TestReadAdaptiveGap_ReturnsStoredValue(t *testing.T) {
	inTempDir(t, func() {
		// 90 секунд — между base(60s) и max(3m) — должно вернуть as-is
		writeGapFile(t, 90*time.Second)
		gap := wintun.ReadAdaptiveGap()
		if gap != 90*time.Second {
			t.Errorf("got %v, want 90s", gap)
		}
	})
}

// ── IncreaseAdaptiveGap ───────────────────────────────────────────────────────

func TestIncreaseAdaptiveGap_DoublesFromBase(t *testing.T) {
	inTempDir(t, func() {
		before := wintun.ReadAdaptiveGap() // 60s, нет файла
		wintun.IncreaseAdaptiveGap(&nullLogger{})
		after := wintun.ReadAdaptiveGap()
		if after != before*2 {
			t.Errorf("ожидали %v (2x), получили %v", before*2, after)
		}
	})
}

func TestIncreaseAdaptiveGap_DoublesRepeatedly(t *testing.T) {
	inTempDir(t, func() {
		// 60 → 120 → 180 (cap)
		wintun.IncreaseAdaptiveGap(&nullLogger{})
		wintun.IncreaseAdaptiveGap(&nullLogger{})
		wintun.IncreaseAdaptiveGap(&nullLogger{})
		gap := wintun.ReadAdaptiveGap()
		if gap != 3*time.Minute {
			t.Errorf("got %v, want 3m after multiple increases", gap)
		}
	})
}

func TestIncreaseAdaptiveGap_CapsAtMax(t *testing.T) {
	inTempDir(t, func() {
		// Форсируем уже максимальное значение
		writeGapFile(t, 3*time.Minute)
		wintun.IncreaseAdaptiveGap(&nullLogger{})
		gap := wintun.ReadAdaptiveGap()
		if gap != 3*time.Minute {
			t.Errorf("got %v, want 3m (should stay at cap)", gap)
		}
	})
}

// ── ResetAdaptiveGap ──────────────────────────────────────────────────────────

func TestResetAdaptiveGap_RemovesFile(t *testing.T) {
	inTempDir(t, func() {
		wintun.IncreaseAdaptiveGap(&nullLogger{})
		wintun.ResetAdaptiveGap()

		if _, err := os.Stat("wintun_gap_ns"); !os.IsNotExist(err) {
			t.Error("gap файл должен быть удалён после Reset")
		}
		if gap := wintun.ReadAdaptiveGap(); gap != 60*time.Second {
			t.Errorf("после reset: got %v, want 60s", gap)
		}
	})
}

func TestResetAdaptiveGap_SafeWhenNoFile(t *testing.T) {
	inTempDir(t, func() {
		// Не должен паниковать если файла нет
		wintun.ResetAdaptiveGap()
	})
}

// ── EstimateReadyAt ───────────────────────────────────────────────────────────

func TestEstimateReadyAt_NoStopFile_ReturnsNow(t *testing.T) {
	inTempDir(t, func() {
		eta := wintun.EstimateReadyAt()
		if eta.After(time.Now().Add(time.Second)) {
			t.Errorf("без StopFile ETA должен быть ≈ now, got %v", eta)
		}
	})
}

func TestEstimateReadyAt_ColdStart_ReturnsNow(t *testing.T) {
	inTempDir(t, func() {
		// Прошло >5 мин — холодный старт, gap не нужен
		writeStopFile(t, time.Now().Add(-10*time.Minute))
		eta := wintun.EstimateReadyAt()
		if eta.After(time.Now().Add(time.Second)) {
			t.Errorf("холодный старт: ETA должен быть ≈ now, got %v", eta)
		}
	})
}

func TestEstimateReadyAt_HotRestart_ReturnsFuture(t *testing.T) {
	inTempDir(t, func() {
		// Только что остановились: ETA должна быть в будущем (консервативная оценка ~35с)
		writeStopFile(t, time.Now())
		eta := wintun.EstimateReadyAt()
		if !eta.After(time.Now()) {
			t.Errorf("горячий рестарт: ETA должен быть в будущем, got %v", eta)
		}
	})
}

func TestEstimateReadyAt_PartialWait_CorrectRemaining(t *testing.T) {
	inTempDir(t, func() {
		// Остановились 30с назад — ETA должна быть в будущем
		writeStopFile(t, time.Now().Add(-30*time.Second))
		eta := wintun.EstimateReadyAt()
		if !eta.After(time.Now()) {
			t.Errorf("partial wait 30s: ETA должен быть в будущем, got %v", eta)
		}
	})
}

func TestEstimateReadyAt_AlreadyPassed_ReturnsNow(t *testing.T) {
	inTempDir(t, func() {
		// Остановились 60с назад — ETA уже в прошлом → возвращаем now
		writeStopFile(t, time.Now().Add(-60*time.Second))
		eta := wintun.EstimateReadyAt()
		if eta.After(time.Now().Add(time.Second)) {
			t.Errorf("gap уже прошёл: ETA должен быть ≈ now, got %v", eta)
		}
	})
}

// ── misc ──────────────────────────────────────────────────────────────────────

func TestStopFile_IsRelativePath(t *testing.T) {
	if wintun.StopFile == "" {
		t.Error("StopFile не должен быть пустым")
	}
	if filepath.IsAbs(wintun.StopFile) {
		t.Errorf("StopFile должен быть относительным, got: %q", wintun.StopFile)
	}
}

// ── nullLogger ────────────────────────────────────────────────────────────────

type nullLogger struct{}

func (n *nullLogger) Debug(_ string, _ ...interface{}) {}
func (n *nullLogger) Info(_ string, _ ...interface{})  {}
func (n *nullLogger) Warn(_ string, _ ...interface{})  {}
func (n *nullLogger) Error(_ string, _ ...interface{}) {}
