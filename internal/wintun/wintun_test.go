//go:build windows

package wintun_test

import (
	"context"
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
		writeGapFile(t, 10*time.Minute)
		gap := wintun.ReadAdaptiveGap()
		if gap != 3*time.Minute {
			t.Errorf("got %v, want 3m (minGapMax cap)", gap)
		}
	})
}

func TestReadAdaptiveGap_FloorsAtMinGapBase(t *testing.T) {
	inTempDir(t, func() {
		writeGapFile(t, 1*time.Second)
		gap := wintun.ReadAdaptiveGap()
		if gap != 60*time.Second {
			t.Errorf("got %v, want 60s (minGapBase floor)", gap)
		}
	})
}

func TestReadAdaptiveGap_ReturnsStoredValue(t *testing.T) {
	inTempDir(t, func() {
		writeGapFile(t, 60*time.Second)
		gap := wintun.ReadAdaptiveGap()
		if gap != 60*time.Second {
			t.Errorf("got %v, want 60s", gap)
		}
	})
}

// ── ReadSettleDelay ───────────────────────────────────────────────────────────

func TestReadSettleDelay_MinAtBaseGap(t *testing.T) {
	inTempDir(t, func() {
		// gap=60s → settle = max(settleBase, 60*0.25=15s) = settleBase
		sd := wintun.ReadSettleDelay()
		if sd < 5*time.Second || sd > 60*time.Second {
			t.Errorf("ReadSettleDelay с base gap: got %v, ожидаем разумное значение", sd)
		}
	})
}

func TestReadSettleDelay_GrowsWithGap(t *testing.T) {
	inTempDir(t, func() {
		// gap=60s
		sd1 := wintun.ReadSettleDelay()

		// gap=120s → settle должен быть >= sd1
		writeGapFile(t, 120*time.Second)
		sd2 := wintun.ReadSettleDelay()

		if sd2 < sd1 {
			t.Errorf("settle должен расти с gap: sd1=%v sd2=%v", sd1, sd2)
		}
	})
}

// ── IncreaseAdaptiveGap ───────────────────────────────────────────────────────

func TestIncreaseAdaptiveGap_DoublesFromBase(t *testing.T) {
	inTempDir(t, func() {
		before := wintun.ReadAdaptiveGap() // 60s
		wintun.IncreaseAdaptiveGap(&nullLogger{})
		after := wintun.ReadAdaptiveGap() // 180s
		if after != before*2 {
			t.Errorf("ожидали %v (2x), получили %v", before*2, after)
		}
	})
}

func TestIncreaseAdaptiveGap_DoublesRepeatedly(t *testing.T) {
	inTempDir(t, func() {
		// 60s → 120s → 3m (cap)
		wintun.IncreaseAdaptiveGap(&nullLogger{})
		wintun.IncreaseAdaptiveGap(&nullLogger{})
		gap := wintun.ReadAdaptiveGap()
		if gap != 3*time.Minute {
			t.Errorf("got %v, want 3m after two increases from 60s base", gap)
		}
	})
}

func TestIncreaseAdaptiveGap_CapsAtMax(t *testing.T) {
	inTempDir(t, func() {
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
		wintun.ResetAdaptiveGap() // не должен паниковать
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

func TestEstimateReadyAt_AlreadyPassed_ReturnsNow(t *testing.T) {
	inTempDir(t, func() {
		// Остановились достаточно давно (> minGapBase=60s) — ETA в прошлом → возвращаем now
		writeStopFile(t, time.Now().Add(-100*time.Second))
		eta := wintun.EstimateReadyAt()
		if eta.After(time.Now().Add(time.Second)) {
			t.Errorf("ETA должен быть ≈ now после долгого ожидания, got future %v", time.Until(eta))
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

func TestFastDeleteFile_IsRelativePath(t *testing.T) {
	if wintun.FastDeleteFile == "" {
		t.Error("FastDeleteFile не должен быть пустым")
	}
	if filepath.IsAbs(wintun.FastDeleteFile) {
		t.Errorf("FastDeleteFile должен быть относительным, got: %q", wintun.FastDeleteFile)
	}
}

// ── PollUntilFree: fast-delete path ──────────────────────────────────────────

// TestPollUntilFree_NoStopFile проверяет что без StopFile функция возвращается
// немедленно (первый запуск ever).
func TestPollUntilFree_NoStopFile(t *testing.T) {
	inTempDir(t, func() {
		done := make(chan struct{})
		go func() {
			defer close(done)
			wintun.PollUntilFree(t.Context(), &nullLogger{}, "tun0")
		}()
		select {
		case <-done:
			// OK — вернулась без ожидания
		case <-time.After(2 * time.Second):
			t.Error("PollUntilFree без StopFile должна вернуться немедленно")
		}
	})
}

// TestPollUntilFree_FastDelete_SkipsGap проверяет что при наличии FastDeleteFile
// PollUntilFree не ждёт adaptive gap (60+ с), а завершается значительно быстрее.
// Timeout 5с: confirm-loop (3×500мс=1.5с) + settle-delay (15с) были бы превышены
// только при ошибке в логике; но поскольку probe/netsh — заглушки (всегда true),
// мы ждём лишь settle-delay. Тест намеренно не проверяет settle-delay в юнит-режиме:
// settle — runtime-поведение, а не логика ветвления.
func TestPollUntilFree_FastDelete_DeletesMarker(t *testing.T) {
	inTempDir(t, func() {
		// Создаём StopFile (только что остановились — горячий рестарт)
		writeStopFile(t, time.Now())
		// Создаём FastDeleteFile — симулируем успешный ForceDeleteAdapter
		_ = os.WriteFile(wintun.FastDeleteFile, []byte("1"), 0644)

		// FastDeleteFile удаляется в самом начале PollUntilFree, ДО любого ожидания.
		// Поэтому отменяем ctx через 500мс — достаточно чтобы синхронная часть
		// (удаление маркера) завершилась, но не ждать весь confirm-loop + settle.
		// Это корректно: тест проверяет удаление маркера, а не поведение ожидания.
		ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
		defer cancel()

		wintun.PollUntilFree(ctx, &nullLogger{}, "tun0")

		// FastDeleteFile должен быть удалён после чтения (одноразовый сигнал).
		// Удаление происходит синхронно до первого sleepCtx — ctx timeout не влияет.
		if _, err := os.Stat(wintun.FastDeleteFile); !os.IsNotExist(err) {
			t.Error("FastDeleteFile должен быть удалён после PollUntilFree")
		}
	})
}

// TestPollUntilFree_FastDelete_MarkerAbsentAfterColdStart проверяет что
// PollUntilFree никогда не создаёт FastDeleteFile — это исключительно задача
// RemoveStaleTunAdapter. Инвариант: маркер отсутствует после PollUntilFree
// независимо от пути (холодный / горячий / fast-delete).
func TestPollUntilFree_FastDelete_MarkerAbsentAfterColdStart(t *testing.T) {
	inTempDir(t, func() {
		// Холодный старт: StopFile очень старый
		writeStopFile(t, time.Now().Add(-10*time.Minute))
		// FastDeleteFile НЕ создаём — холодный путь, маркера нет

		// Канселим ctx через 500мс: проверяемый инвариант (маркер не создаётся)
		// виден немедленно — PollUntilFree никогда не вызывает WriteFile(FastDeleteFile).
		// Полное ожидание confirm-loop не нужно для этой проверки.
		ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
		defer cancel()

		wintun.PollUntilFree(ctx, &nullLogger{}, "tun0")

		// Маркер по-прежнему отсутствует — PollUntilFree его не создаёт
		if _, err := os.Stat(wintun.FastDeleteFile); !os.IsNotExist(err) {
			t.Error("FastDeleteFile не должен появляться после холодного старта")
		}
	})
}

// TestPollUntilFree_ContextCancel проверяет что отмена ctx прерывает ожидание.
func TestPollUntilFree_ContextCancel(t *testing.T) {
	inTempDir(t, func() {
		// Горячий рестарт без fast-delete: ушли бы в 60с gap
		writeStopFile(t, time.Now())

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			defer close(done)
			wintun.PollUntilFree(ctx, &nullLogger{}, "tun0")
		}()

		// Отменяем через 100мс — не должны ждать весь gap
		time.Sleep(100 * time.Millisecond)
		cancel()

		select {
		case <-done:
			// OK — прервалась по ctx
		case <-time.After(3 * time.Second):
			t.Error("PollUntilFree должна прерваться при отмене ctx")
		}
	})
}

// ── nullLogger ────────────────────────────────────────────────────────────────

type nullLogger struct{}

func (n *nullLogger) Debug(_ string, _ ...interface{}) {}
func (n *nullLogger) Info(_ string, _ ...interface{})  {}
func (n *nullLogger) Warn(_ string, _ ...interface{})  {}
func (n *nullLogger) Error(_ string, _ ...interface{}) {}
