//go:build windows
// +build windows

package wintun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── SleepCtx Tests ────────────────────────────────────────────────────────────────

func TestSleepCtx_ReturnsTrue_OnNormalCompletion(t *testing.T) {
	ctx := context.Background()

	result := SleepCtx(ctx, 10*time.Millisecond)
	if !result {
		t.Error("SleepCtx should return true on normal completion")
	}
}

func TestSleepCtx_ReturnsFalse_OnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	result := SleepCtx(ctx, 100*time.Millisecond)
	if result {
		t.Error("SleepCtx should return false on context cancellation")
	}
}

func TestSleepCtx_ReturnsFalse_OnContextTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	result := SleepCtx(ctx, 100*time.Millisecond)
	if result {
		t.Error("SleepCtx should return false on context timeout")
	}
}

func TestSleepCtx_ZeroDuration(t *testing.T) {
	ctx := context.Background()

	result := SleepCtx(ctx, 0)
	if !result {
		t.Error("SleepCtx with zero duration should return true immediately")
	}
}

func TestSleepCtx_NegativeDuration(t *testing.T) {
	ctx := context.Background()

	// Negative duration should behave like zero
	result := SleepCtx(ctx, -1*time.Second)
	if !result {
		t.Error("SleepCtx with negative duration should return true immediately")
	}
}

// ── InterfaceExists Tests ─────────────────────────────────────────────────────────

func TestInterfaceExists_ReturnsFalse_ForNonExistent(t *testing.T) {
	// Use a very unlikely interface name
	result := InterfaceExists("NonExistentInterface12345")
	if result {
		t.Error("InterfaceExists should return false for non-existent interface")
	}
}

// ── NetAdapterExists Tests ────────────────────────────────────────────────────────

func TestNetAdapterExists_ReturnsFalse_ForNonExistent(t *testing.T) {
	// NetAdapterExists queries PnP for wintun/WireGuard/sing-tun devices globally.
	// If the system has any such adapter, the result is true regardless of the name passed.
	// Skip when real wintun/WireGuard/sing-tun adapters are present.
	if NetAdapterExists("__probe_nonexistent_xyz__") {
		t.Skip("system has wintun/WireGuard/sing-tun adapters installed; test not meaningful")
	}
	result := NetAdapterExists("NonExistentAdapter12345")
	if result {
		t.Error("NetAdapterExists should return false for non-existent adapter")
	}
}

// ── CleanSCOutput Tests ───────────────────────────────────────────────────────────

func TestCleanSCOutput_ExtractsError(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains string
	}{
		{"simple error", "ERROR: The service does not exist", "does not exist"},
		{"success message", "[SC] DeleteService SUCCESS", ""},
		{"empty", "", ""},
		{"multi-line", "Line 1\nERROR: something wrong\nLine 3", "something wrong"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := CleanSCOutput(tc.input)
			if tc.contains != "" && !strings.Contains(result, tc.contains) {
				t.Errorf("CleanSCOutput(%q) = %q, should contain %q", tc.input, result, tc.contains)
			}
		})
	}
}

// ── Adaptive Gap Tests ────────────────────────────────────────────────────────────

func TestReadAdaptiveGap_ReturnsBase_WhenFileMissing(t *testing.T) {
	dir := t.TempDir()
	gapFile := filepath.Join(dir, "gap.txt")

	gap := ReadAdaptiveGap(gapFile)
	if gap != MinGapBase {
		t.Errorf("ReadAdaptiveGap with missing file = %v, want %v", gap, MinGapBase)
	}
}

func TestReadAdaptiveGap_ReturnsValue_WhenValid(t *testing.T) {
	dir := t.TempDir()
	gapFile := filepath.Join(dir, "gap.txt")

	// Write valid value above MinGapBase so it's not clamped
	os.WriteFile(gapFile, []byte("2m"), 0644)

	gap := ReadAdaptiveGap(gapFile)
	if gap != 2*time.Minute {
		t.Errorf("ReadAdaptiveGap = %v, want 2m", gap)
	}
}

func TestReadAdaptiveGap_ReturnsBase_WhenInvalidContent(t *testing.T) {
	dir := t.TempDir()
	gapFile := filepath.Join(dir, "gap.txt")

	// Write invalid content
	os.WriteFile(gapFile, []byte("invalid"), 0644)

	gap := ReadAdaptiveGap(gapFile)
	if gap != MinGapBase {
		t.Errorf("ReadAdaptiveGap with invalid content = %v, want %v", gap, MinGapBase)
	}
}

func TestReadAdaptiveGap_ClampsToMax(t *testing.T) {
	dir := t.TempDir()
	gapFile := filepath.Join(dir, "gap.txt")

	// Write value exceeding max
	os.WriteFile(gapFile, []byte("10h"), 0644)

	gap := ReadAdaptiveGap(gapFile)
	if gap > MaxGap {
		t.Errorf("ReadAdaptiveGap = %v, should be <= %v", gap, MaxGap)
	}
}

func TestReadAdaptiveGap_ClampsToMin(t *testing.T) {
	dir := t.TempDir()
	gapFile := filepath.Join(dir, "gap.txt")

	// Write value below minimum
	os.WriteFile(gapFile, []byte("100ms"), 0644)

	gap := ReadAdaptiveGap(gapFile)
	if gap < MinGapBase {
		t.Errorf("ReadAdaptiveGap = %v, should be >= %v", gap, MinGapBase)
	}
}

// ── IncreaseAdaptiveGap Tests ──────────────────────────────────────────────────────

func TestIncreaseAdaptiveGap_DoublesFromBase(t *testing.T) {
	dir := t.TempDir()
	gapFile := filepath.Join(dir, "gap.txt")

	// No existing file, should start from base
	gap := IncreaseAdaptiveGap(gapFile)
	expected := MinGapBase * 2

	if gap != expected {
		t.Errorf("IncreaseAdaptiveGap first call = %v, want %v", gap, expected)
	}
}

func TestIncreaseAdaptiveGap_DoublesRepeatedly(t *testing.T) {
	dir := t.TempDir()
	gapFile := filepath.Join(dir, "gap.txt")

	prevGap := MinGapBase
	for i := 0; i < 5; i++ {
		gap := IncreaseAdaptiveGap(gapFile)
		// Gap should double unless both prev and current are at MaxGap (capped)
		if gap != prevGap*2 && !(gap == MaxGap && prevGap >= MaxGap/2) {
			t.Errorf("Gap should double: prev=%v, got=%v", prevGap, gap)
		}
		prevGap = gap
	}
}

func TestIncreaseAdaptiveGap_ClampsToMax(t *testing.T) {
	dir := t.TempDir()
	gapFile := filepath.Join(dir, "gap.txt")

	// Write value close to max
	os.WriteFile(gapFile, []byte(MaxGap.String()), 0644)

	gap := IncreaseAdaptiveGap(gapFile)
	if gap > MaxGap {
		t.Errorf("IncreaseAdaptiveGap = %v, should be <= %v", gap, MaxGap)
	}
}

// ── ResetAdaptiveGap Tests ─────────────────────────────────────────────────────────

func TestResetAdaptiveGap_RemovesFile(t *testing.T) {
	dir := t.TempDir()
	gapFile := filepath.Join(dir, "gap.txt")

	// Create file
	os.WriteFile(gapFile, []byte("10s"), 0644)

	ResetAdaptiveGap(gapFile)

	if _, err := os.Stat(gapFile); !os.IsNotExist(err) {
		t.Error("ResetAdaptiveGap should remove gap file")
	}
}

func TestResetAdaptiveGap_SafeWhenNoFile(t *testing.T) {
	dir := t.TempDir()
	gapFile := filepath.Join(dir, "gap.txt")

	// Should not panic when file doesn't exist
	ResetAdaptiveGap(gapFile)
}

// ── RecordStop Tests ───────────────────────────────────────────────────────────────

func TestRecordStop_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	stopFile := filepath.Join(dir, "stop.txt")

	RecordStop(stopFile)

	if _, err := os.Stat(stopFile); os.IsNotExist(err) {
		t.Error("RecordStop should create stop file")
	}
}

func TestRecordStop_OverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	stopFile := filepath.Join(dir, "stop.txt")

	// Create existing file
	os.WriteFile(stopFile, []byte("old content"), 0644)

	RecordStop(stopFile)

	// File should exist (with new timestamp)
	if _, err := os.Stat(stopFile); os.IsNotExist(err) {
		t.Error("RecordStop should maintain stop file")
	}
}

// ── RecordCleanShutdown Tests ──────────────────────────────────────────────────────

func TestRecordCleanShutdown_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	cleanFile := filepath.Join(dir, "clean.txt")

	RecordCleanShutdown(cleanFile)

	if _, err := os.Stat(cleanFile); os.IsNotExist(err) {
		t.Error("RecordCleanShutdown should create clean shutdown file")
	}
}

// ── EstimateReadyAt Tests ──────────────────────────────────────────────────────────

func TestEstimateReadyAt_ReturnsNow_WhenCleanShutdown(t *testing.T) {
	dir := t.TempDir()
	stopFile := filepath.Join(dir, "stop.txt")
	cleanFile := filepath.Join(dir, "clean.txt")
	gapFile := filepath.Join(dir, "gap.txt")

	// Record clean shutdown
	RecordCleanShutdown(cleanFile)

	readyAt := EstimateReadyAtWithFiles(stopFile, cleanFile, gapFile)
	now := time.Now()

	// Should be approximately now (within 1 second)
	if readyAt.After(now.Add(time.Second)) || readyAt.Before(now.Add(-time.Second)) {
		t.Errorf("EstimateReadyAt with clean shutdown = %v, want approximately %v", readyAt, now)
	}
}

// ── Settle Delay Tests ─────────────────────────────────────────────────────────────

func TestReadSettleDelay_MinAtBaseGap(t *testing.T) {
	dir := t.TempDir()
	gapFile := filepath.Join(dir, "gap.txt")

	// No gap file = base gap
	settle := ReadSettleDelay(gapFile)

	if settle < MinSettleDelay {
		t.Errorf("Settle delay = %v, should be >= %v", settle, MinSettleDelay)
	}
}

func TestReadSettleDelay_GrowsWithGap(t *testing.T) {
	dir := t.TempDir()
	gapFile := filepath.Join(dir, "gap.txt")

	// Write large gap
	os.WriteFile(gapFile, []byte("30s"), 0644)

	settle := ReadSettleDelay(gapFile)

	// Settle should be proportional
	if settle < MinSettleDelay {
		t.Errorf("Settle delay = %v, should be >= %v", settle, MinSettleDelay)
	}
}

func TestReadSettleDelay_MaxCap(t *testing.T) {
	dir := t.TempDir()
	gapFile := filepath.Join(dir, "gap.txt")

	// Write very large gap
	os.WriteFile(gapFile, []byte("10m"), 0644)

	settle := ReadSettleDelay(gapFile)

	if settle > MaxSettleDelay {
		t.Errorf("Settle delay = %v, should be <= %v", settle, MaxSettleDelay)
	}
}

// ── PollUntilFree Tests ────────────────────────────────────────────────────────────

func TestPollUntilFree_ReturnsImmediately_WhenCleanShutdown(t *testing.T) {
	dir := t.TempDir()
	stopFile := filepath.Join(dir, "stop.txt")
	cleanFile := filepath.Join(dir, "clean.txt")
	gapFile := filepath.Join(dir, "gap.txt")

	// Record clean shutdown
	RecordCleanShutdown(cleanFile)

	start := time.Now()
	err := PollUntilFreeWithFiles(context.Background(), stopFile, cleanFile, gapFile)
	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("PollUntilFree returned error: %v", err)
	}

	// Should return almost immediately
	if elapsed > 500*time.Millisecond {
		t.Errorf("PollUntilFree took %v with clean shutdown, should be faster", elapsed)
	}
}

func TestPollUntilFree_ContextCancellation(t *testing.T) {
	dir := t.TempDir()
	stopFile := filepath.Join(dir, "stop.txt")
	cleanFile := filepath.Join(dir, "clean.txt")
	gapFile := filepath.Join(dir, "gap.txt")

	// Create stop file to simulate hot start
	RecordStop(stopFile)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := PollUntilFreeWithFiles(ctx, stopFile, cleanFile, gapFile)
	elapsed := time.Since(start)

	if err == nil {
		t.Error("PollUntilFree should return error on context cancellation")
	}

	// Should respect context timeout
	if elapsed > 200*time.Millisecond {
		t.Errorf("PollUntilFree took %v, should respect context timeout", elapsed)
	}
}

// ── Concurrent Tests ──────────────────────────────────────────────────────────────

func TestReadAdaptiveGap_Concurrent(t *testing.T) {
	dir := t.TempDir()
	gapFile := filepath.Join(dir, "gap.txt")

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ReadAdaptiveGap(gapFile)
		}()
	}
	wg.Wait()
}

func TestIncreaseAdaptiveGap_Concurrent(t *testing.T) {
	dir := t.TempDir()
	gapFile := filepath.Join(dir, "gap.txt")

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			IncreaseAdaptiveGap(gapFile)
		}()
	}
	wg.Wait()
}

func TestRecordStop_Concurrent(t *testing.T) {
	dir := t.TempDir()
	stopFile := filepath.Join(dir, "stop.txt")

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			RecordStop(stopFile)
		}()
	}
	wg.Wait()

	// File should exist
	if _, err := os.Stat(stopFile); os.IsNotExist(err) {
		t.Error("Stop file should exist after concurrent writes")
	}
}

// ── Fuzz Tests ────────────────────────────────────────────────────────────────────

func FuzzReadAdaptiveGap(f *testing.F) {
	seeds := []string{
		"1s",
		"5s",
		"10s",
		"invalid",
		"",
		"1h",
		"-1s",
		"0",
	}

	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, content string) {
		dir := t.TempDir()
		gapFile := filepath.Join(dir, "gap.txt")

		os.WriteFile(gapFile, []byte(content), 0644)

		// Should not panic
		gap := ReadAdaptiveGap(gapFile)

		// Should be within valid bounds
		if gap < MinGapBase || gap > MaxGap {
			t.Errorf("Gap %v outside valid range [%v, %v]", gap, MinGapBase, MaxGap)
		}
	})
}

func FuzzCleanSCOutput(f *testing.F) {
	seeds := []string{
		"SUCCESS",
		"ERROR: failed",
		"",
		"Multi\nline\noutput",
		"ERROR: \x00 null byte",
	}

	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, output string) {
		// Should not panic
		result := CleanSCOutput(output)
		_ = result
	})
}

// ── Benchmark Tests ──────────────────────────────────────────────────────────────

func BenchmarkReadAdaptiveGap(b *testing.B) {
	dir := b.TempDir()
	gapFile := filepath.Join(dir, "gap.txt")
	os.WriteFile(gapFile, []byte("5s"), 0644)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ReadAdaptiveGap(gapFile)
	}
}

func BenchmarkIncreaseAdaptiveGap(b *testing.B) {
	dir := b.TempDir()
	gapFile := filepath.Join(dir, "gap.txt")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		IncreaseAdaptiveGap(gapFile)
	}
}

func BenchmarkRecordStop(b *testing.B) {
	dir := b.TempDir()
	stopFile := filepath.Join(dir, "stop.txt")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		RecordStop(stopFile)
	}
}

func BenchmarkEstimateReadyAt(b *testing.B) {
	dir := b.TempDir()
	stopFile := filepath.Join(dir, "stop.txt")
	cleanFile := filepath.Join(dir, "clean.txt")
	gapFile := filepath.Join(dir, "gap.txt")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		EstimateReadyAtWithFiles(stopFile, cleanFile, gapFile)
	}
}

// ── Table-driven tests ────────────────────────────────────────────────────────────

func TestReadAdaptiveGap_Table(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected time.Duration
	}{
		{"1 second", "1s", time.Second},
		{"5 seconds", "5s", 5 * time.Second},
		{"10 seconds", "10s", 10 * time.Second},
		{"1 minute", "1m", time.Minute},
		{"empty", "", MinGapBase},
		{"invalid", "invalid", MinGapBase},
		{"too small", "100ms", MinGapBase},
		{"too large", "10h", MaxGap},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			gapFile := filepath.Join(dir, "gap.txt")

			if tc.content != "" {
				os.WriteFile(gapFile, []byte(tc.content), 0644)
			}

			gap := ReadAdaptiveGap(gapFile)

			// Allow for clamping
			if tc.expected >= MinGapBase && tc.expected <= MaxGap {
				if gap != tc.expected {
					t.Errorf("ReadAdaptiveGap = %v, want %v", gap, tc.expected)
				}
			}
		})
	}
}

// ── Edge Cases ────────────────────────────────────────────────────────────────────

func TestReadAdaptiveGap_CorruptFile(t *testing.T) {
	dir := t.TempDir()
	gapFile := filepath.Join(dir, "gap.txt")

	// Write binary garbage
	os.WriteFile(gapFile, []byte{0x00, 0x01, 0x02, 0xFF}, 0644)

	gap := ReadAdaptiveGap(gapFile)
	if gap != MinGapBase {
		t.Errorf("ReadAdaptiveGap with corrupt file = %v, want %v", gap, MinGapBase)
	}
}

func TestReadAdaptiveGap_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	gapFile := filepath.Join(dir, "gap.txt")

	os.WriteFile(gapFile, []byte{}, 0644)

	gap := ReadAdaptiveGap(gapFile)
	if gap != MinGapBase {
		t.Errorf("ReadAdaptiveGap with empty file = %v, want %v", gap, MinGapBase)
	}
}

func TestIncreaseAdaptiveGap_FromZero(t *testing.T) {
	dir := t.TempDir()
	gapFile := filepath.Join(dir, "gap.txt")

	// Write zero gap
	os.WriteFile(gapFile, []byte("0s"), 0644)

	gap := IncreaseAdaptiveGap(gapFile)
	// Should start from base
	if gap < MinGapBase {
		t.Errorf("IncreaseAdaptiveGap from zero = %v, should be >= %v", gap, MinGapBase)
	}
}

// ── Constants verification ─────────────────────────────────────────────────────────

func TestConstants(t *testing.T) {
	// Verify constants are reasonable
	if MinGapBase <= 0 {
		t.Error("MinGapBase should be positive")
	}
	if MaxGap <= MinGapBase {
		t.Error("MaxGap should be greater than MinGapBase")
	}
	if MinSettleDelay <= 0 {
		t.Error("MinSettleDelay should be positive")
	}
	if MaxSettleDelay <= MinSettleDelay {
		t.Error("MaxSettleDelay should be greater than MinSettleDelay")
	}
}

// ── Integration-style tests ────────────────────────────────────────────────────────

func TestAdaptiveGap_FullLifecycle(t *testing.T) {
	dir := t.TempDir()
	gapFile := filepath.Join(dir, "gap.txt")

	// Initial read
	gap := ReadAdaptiveGap(gapFile)
	if gap != MinGapBase {
		t.Errorf("Initial gap = %v, want %v", gap, MinGapBase)
	}

	// Increase several times
	for i := 0; i < 5; i++ {
		gap = IncreaseAdaptiveGap(gapFile)
	}

	// Should have increased
	finalGap := ReadAdaptiveGap(gapFile)
	if finalGap <= MinGapBase {
		t.Errorf("After increases, gap = %v, should be > %v", finalGap, MinGapBase)
	}

	// Reset
	ResetAdaptiveGap(gapFile)

	// Should be back to base
	gap = ReadAdaptiveGap(gapFile)
	if gap != MinGapBase {
		t.Errorf("After reset, gap = %v, want %v", gap, MinGapBase)
	}
}

func TestStopAndCleanShutdown_Coexistence(t *testing.T) {
	dir := t.TempDir()
	stopFile := filepath.Join(dir, "stop.txt")
	cleanFile := filepath.Join(dir, "clean.txt")

	// Record stop
	RecordStop(stopFile)

	// Record clean shutdown (should take priority)
	RecordCleanShutdown(cleanFile)

	// Both files should exist
	if _, err := os.Stat(stopFile); os.IsNotExist(err) {
		t.Error("Stop file should exist")
	}
	if _, err := os.Stat(cleanFile); os.IsNotExist(err) {
		t.Error("Clean shutdown file should exist")
	}
}
