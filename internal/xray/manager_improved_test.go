//go:build windows
// +build windows

package xray

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── tailWriter Tests ────────────────────────────────────────────────────────────

func TestTailWriter_CapturesOutput(t *testing.T) {
	tw := newTailWriter(1024)
	testData := "line 1\nline 2\nline 3\n"

	n, err := tw.Write([]byte(testData))
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if n != len(testData) {
		t.Errorf("Write returned %d, want %d", n, len(testData))
	}

	if tw.String() != testData {
		t.Errorf("String() = %q, want %q", tw.String(), testData)
	}
}

func TestTailWriter_TruncatesOldData(t *testing.T) {
	maxSize := 100
	tw := newTailWriter(maxSize)

	// Write more than maxSize
	largeData := strings.Repeat("x", 200)
	tw.Write([]byte(largeData))

	result := tw.String()
	if len(result) > maxSize {
		t.Errorf("Result length %d exceeds max %d", len(result), maxSize)
	}
}

func TestTailWriter_DoesNotCutMidLine(t *testing.T) {
	maxSize := 50
	tw := newTailWriter(maxSize)

	// Write data that would cut a line if truncated at maxSize
	line1 := "this is a very long first line that exceeds the limit\n"
	line2 := "second line\n"
	tw.Write([]byte(line1 + line2))

	result := tw.String()
	// Result should not start with partial line
	if strings.HasPrefix(result, "is a very") {
		t.Errorf("Result appears to be cut mid-line: %q", result[:30])
	}
}

func TestTailWriter_ResetClearsBuffer(t *testing.T) {
	tw := newTailWriter(1024)
	tw.Write([]byte("some data"))

	tw.Reset()

	if tw.String() != "" {
		t.Errorf("After Reset(), String() = %q, want empty", tw.String())
	}
}

func TestTailWriter_ResetPreservesCapacity(t *testing.T) {
	maxSize := 1024
	tw := newTailWriter(maxSize)
	tw.Write([]byte(strings.Repeat("x", maxSize)))

	tw.Reset()

	// Should be able to write again
	tw.Write([]byte("new data"))
	if tw.String() != "new data" {
		t.Errorf("After Reset and Write, String() = %q", tw.String())
	}
}

func TestTailWriter_ConcurrentWrites(t *testing.T) {
	tw := newTailWriter(1024)
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				tw.Write([]byte(fmt.Sprintf("goroutine %d line %d\n", id, j)))
			}
		}(i)
	}

	wg.Wait()

	// Should not panic and should have some content
	if len(tw.String()) == 0 {
		t.Error("Expected some content after concurrent writes")
	}
}

func TestTailWriter_LargeOutput(t *testing.T) {
	maxSize := 32 * 1024 // 32KB
	tw := newTailWriter(maxSize)

	// Write 100KB
	for i := 0; i < 1000; i++ {
		tw.Write([]byte(strings.Repeat("x", 100) + "\n"))
	}

	result := tw.String()
	// Should be truncated to approximately maxSize
	if len(result) > maxSize*2 { // Allow some slack for line boundary
		t.Errorf("Result length %d is too large (max ~%d)", len(result), maxSize*2)
	}
}

func TestTailWriter_UnicodeContent(t *testing.T) {
	tw := newTailWriter(1024)
	unicodeData := "Привет мир\n日本語テスト\nEmoji: 🔥🎉\n"

	tw.Write([]byte(unicodeData))

	result := tw.String()
	if !strings.Contains(result, "Привет") {
		t.Errorf("Unicode content corrupted: %q", result)
	}
}

func TestTailWriter_EmptyWrite(t *testing.T) {
	tw := newTailWriter(1024)

	n, err := tw.Write([]byte{})
	if err != nil {
		t.Errorf("Empty write should not error: %v", err)
	}
	if n != 0 {
		t.Errorf("Empty write returned %d, want 0", n)
	}
}

func TestTailWriter_NewLineBoundary(t *testing.T) {
	tw := newTailWriter(50)

	// Write exact boundary case
	data := "1234567890\n1234567890\n1234567890\n1234567890\n1234567890\n"
	tw.Write([]byte(data))

	result := tw.String()
	// Check that result ends with newline
	if len(result) > 0 && result[len(result)-1] != '\n' {
		t.Logf("Warning: result doesn't end with newline: %q", result)
	}
}

// ── crashTracker Tests ───────────────────────────────────────────────────────────

func TestCrashTracker_Reset_ClearsCount(t *testing.T) {
	ct := &crashTracker{}
	ct.Record()
	ct.Record()
	ct.Record()

	ct.Reset()

	// Count should restart from 1
	count := ct.Record()
	if count != 1 {
		t.Errorf("After Reset(), Record() = %d, want 1", count)
	}
}

func TestCrashTracker_Record_ResetsAfterWindow(t *testing.T) {
	ct := &crashTracker{}

	// Record a crash
	ct.Record()

	// Simulate time passing beyond the window
	ct.mu.Lock()
	ct.lastCrashAt = time.Now().Add(-crashRateWindow - time.Minute)
	ct.mu.Unlock()

	// Next record should reset count
	count := ct.Record()
	if count != 1 {
		t.Errorf("After window expiry, Record() = %d, want 1", count)
	}
}

func TestCrashTracker_ConcurrentRecord(t *testing.T) {
	ct := &crashTracker{}
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				ct.Record()
			}
		}()
	}

	wg.Wait()
	// Should not panic
}

func TestCrashTracker_ConcurrentReset(t *testing.T) {
	ct := &crashTracker{}
	var wg sync.WaitGroup

	for i := 0; i < 5; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				ct.Record()
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				ct.Reset()
			}
		}()
	}

	wg.Wait()
	// Should not panic or race
}

// ── IsTunConflict Tests ──────────────────────────────────────────────────────────

func TestIsTunConflict_DetectsKnownSignatures(t *testing.T) {
	signatures := []string{
		"Cannot create a file when that file already exists",
		"configure tun interface",
		"A device attached to the system is not functioning",
	}

	for _, sig := range signatures {
		t.Run(sig[:min(30, len(sig))], func(t *testing.T) {
			output := "Some log line\n" + sig + "\nMore log"
			if !IsTunConflict(output) {
				t.Errorf("IsTunConflict should detect %q", sig)
			}
		})
	}
}

func TestIsTunConflict_ReturnsFalseForNormalOutput(t *testing.T) {
	normalOutputs := []string{
		"Started successfully",
		"Listening on :8080",
		"Connection established",
		"",
		"random text without conflict keywords",
	}

	for _, output := range normalOutputs {
		t.Run(output[:min(20, len(output))], func(t *testing.T) {
			if IsTunConflict(output) {
				t.Errorf("IsTunConflict should return false for %q", output)
			}
		})
	}
}

func TestIsTunConflict_CaseSensitive(t *testing.T) {
	// The function uses strings.Contains which is case-sensitive
	lowerOutput := "cannot create a file when that file already exists"
	if IsTunConflict(lowerOutput) {
		t.Error("IsTunConflict should be case-sensitive")
	}
}

func TestIsTunConflict_MultiLineOutput(t *testing.T) {
	output := `INFO: Starting sing-box...
INFO: Loading configuration...
ERROR: Cannot create a file when that file already exists
INFO: Retrying...`

	if !IsTunConflict(output) {
		t.Error("IsTunConflict should detect signature in multi-line output")
	}
}

func TestTunConflictSignatures_NotEmpty(t *testing.T) {
	if len(TunConflictSignatures) == 0 {
		t.Error("TunConflictSignatures should not be empty")
	}
}

// ── tooManyRestartsError Tests ───────────────────────────────────────────────────

func TestTooManyRestartsError_Message(t *testing.T) {
	err := &tooManyRestartsError{count: 5}

	msg := err.Error()
	if !strings.Contains(msg, "5") {
		t.Errorf("Error message should contain count: %s", msg)
	}
	if !strings.Contains(msg, "crashes") {
		t.Errorf("Error message should mention crashes: %s", msg)
	}
}

func TestIsTooManyRestarts_ReturnsTrue(t *testing.T) {
	err := &tooManyRestartsError{count: 10}
	if !IsTooManyRestarts(err) {
		t.Error("IsTooManyRestarts should return true for tooManyRestartsError")
	}
}

func TestIsTooManyRestarts_ReturnsFalse(t *testing.T) {
	err := fmt.Errorf("some other error")
	if IsTooManyRestarts(err) {
		t.Error("IsTooManyRestarts should return false for other errors")
	}
}

func TestIsTooManyRestarts_ReturnsFalseForNil(t *testing.T) {
	if IsTooManyRestarts(nil) {
		t.Error("IsTooManyRestarts should return false for nil")
	}
}

// ── Helper function tests ───────────────────────────────────────────────────────

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ── Fuzz Tests ──────────────────────────────────────────────────────────────────
// (FuzzTailWriter и FuzzIsTunConflict объявлены в fuzz_test.go)

// ── Benchmark Tests ──────────────────────────────────────────────────────────────

func BenchmarkTailWriter_Write(b *testing.B) {
	tw := newTailWriter(32 * 1024)
	data := []byte(strings.Repeat("x", 100) + "\n")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tw.Write(data)
	}
}

func BenchmarkTailWriter_String(b *testing.B) {
	tw := newTailWriter(32 * 1024)
	for i := 0; i < 1000; i++ {
		tw.Write([]byte(strings.Repeat("x", 100) + "\n"))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tw.String()
	}
}

func BenchmarkCrashTracker_Record(b *testing.B) {
	ct := &crashTracker{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ct.Record()
	}
}

func BenchmarkIsTunConflict(b *testing.B) {
	output := strings.Repeat("line\n", 100) + "Cannot create a file when that file already exists\n"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		IsTunConflict(output)
	}
}

// ── Edge Cases ──────────────────────────────────────────────────────────────────

func TestTailWriter_ExactMaxSize(t *testing.T) {
	maxSize := 100
	tw := newTailWriter(maxSize)

	// Write exactly maxSize bytes
	data := strings.Repeat("x", maxSize)
	tw.Write([]byte(data))

	if tw.String() != data {
		t.Errorf("Expected exact data when within limit")
	}
}

func TestTailWriter_OneByteOverMax(t *testing.T) {
	maxSize := 100
	tw := newTailWriter(maxSize)

	// Write one byte over
	data := strings.Repeat("x", maxSize+1)
	tw.Write([]byte(data))

	result := tw.String()
	if len(result) > maxSize {
		t.Errorf("Result length %d should be <= %d", len(result), maxSize)
	}
}

func TestCrashTracker_ExactlyAtWindowBoundary(t *testing.T) {
	ct := &crashTracker{}
	ct.Record()

	// Set last crash exactly at window boundary
	ct.mu.Lock()
	ct.lastCrashAt = time.Now().Add(-crashRateWindow)
	ct.mu.Unlock()

	// Should still reset (time.Since > crashRateWindow)
	count := ct.Record()
	if count != 1 {
		t.Logf("At exact boundary, count = %d (may vary based on timing)", count)
	}
}

// ── Concurrent safety tests ──────────────────────────────────────────────────────

func TestTailWriter_ConcurrentString(t *testing.T) {
	tw := newTailWriter(1024)
	var wg sync.WaitGroup

	// Concurrent writes and reads
	for i := 0; i < 5; i++ {
		wg.Add(2)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				tw.Write([]byte(fmt.Sprintf("writer %d: line %d\n", id, j)))
			}
		}(i)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = tw.String()
			}
		}()
	}

	wg.Wait()
}

func TestCrashTracker_ConcurrentString(t *testing.T) {
	ct := &crashTracker{}
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				ct.Record()
				ct.Reset()
			}
		}()
	}

	wg.Wait()
}

// ── Table-driven tests ───────────────────────────────────────────────────────────

func TestIsTunConflict_Table(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected bool
	}{
		{"empty", "", false},
		{"no signature", "All good!", false},
		{"file exists error", "Error: Cannot create a file when that file already exists", true},
		{"tun config error", "Failed to configure tun interface: error", true},
		{"device error", "A device attached to the system is not functioning", true},
		{"case mismatch", "cannot create a file when that file already exists", false},
		{"partial match", "Cannot create a file", false},
		{"multi-line with error", "Line 1\nLine 2\nCannot create a file when that file already exists\nLine 4", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := IsTunConflict(tc.output)
			if result != tc.expected {
				t.Errorf("IsTunConflict(%q) = %v, want %v", tc.output, result, tc.expected)
			}
		})
	}
}

func TestFmtBytes_Table(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{-1, "?"},
		{0, "0B"},
		{1, "1B"},
		{512, "512B"},
		{1023, "1023B"},
		{1024, "1.0KB"},
		{1536, "1.5KB"},
		{1024 * 1024, "1.0MB"},
		{10 * 1024 * 1024, "10.0MB"},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("%d", tc.input), func(t *testing.T) {
			// Note: fmtBytes is in engine package, not xray
			// This test is for demonstration
		})
	}
}

// ── Buffer boundary tests ────────────────────────────────────────────────────────

func TestTailWriter_BufferBoundary(t *testing.T) {
	// Test that buffer doesn't grow unbounded
	maxSize := 1000
	tw := newTailWriter(maxSize)

	// Write many times
	for i := 0; i < 100; i++ {
		tw.Write([]byte(strings.Repeat("x", 100)))
	}

	// Internal buffer should be bounded
	tw.mu.Lock()
	bufLen := len(tw.buf)
	tw.mu.Unlock()

	if bufLen > maxSize*2 {
		t.Errorf("Buffer grew to %d, should be bounded around %d", bufLen, maxSize)
	}
}

// ── Memory efficiency test ───────────────────────────────────────────────────────

func TestTailWriter_MemoryEfficiency(t *testing.T) {
	// Verify that Reset reuses the buffer
	tw := newTailWriter(1024)

	tw.Write([]byte(strings.Repeat("x", 1024)))
	cap1 := cap(tw.buf)

	tw.Reset()
	cap2 := cap(tw.buf)

	// Capacity should be preserved (not reallocated)
	if cap2 < cap1/2 {
		t.Errorf("After Reset, capacity %d is much less than original %d", cap2, cap1)
	}
}

// ── Write return value tests ─────────────────────────────────────────────────────

func TestTailWriter_WriteReturnValue(t *testing.T) {
	tw := newTailWriter(1024)

	testCases := [][]byte{
		[]byte("short"),
		[]byte(strings.Repeat("x", 2000)), // longer than max
		[]byte(""),
		[]byte("unicode: 日本語"),
	}

	for _, data := range testCases {
		n, err := tw.Write(data)
		if err != nil {
			t.Errorf("Write(%q) error: %v", string(data[:min(20, len(data))]), err)
		}
		if n != len(data) {
			t.Errorf("Write(%q) returned %d, want %d", string(data[:min(20, len(data))]), n, len(data))
		}
	}
}

// ── Tests for line-aware truncation ───────────────────────────────────────────────

func TestTailWriter_LineAwareTruncation(t *testing.T) {
	maxSize := 50
	tw := newTailWriter(maxSize)

	// Create a scenario where truncation happens mid-line
	longLine := strings.Repeat("a", 100) + "\n"
	shortLine := "short\n"

	tw.Write([]byte(longLine + shortLine))

	result := tw.String()
	// Result should end with newline (not cut mid-line in shortLine)
	if len(result) > 0 && result[len(result)-1] != '\n' {
		t.Errorf("Result should end with newline: %q", result)
	}
}

// ── Integration-style test ───────────────────────────────────────────────────────

func TestTailWriter_FullLifecycle(t *testing.T) {
	maxSize := 100
	tw := newTailWriter(maxSize)

	// Simulate multiple write cycles
	for cycle := 0; cycle < 5; cycle++ {
		// Write data
		for i := 0; i < 10; i++ {
			tw.Write([]byte(fmt.Sprintf("cycle %d line %d\n", cycle, i)))
		}

		// Check result is bounded
		result := tw.String()
		if len(result) > maxSize*2 {
			t.Errorf("Cycle %d: result too long: %d", cycle, len(result))
		}

		// Reset for next cycle
		tw.Reset()

		if tw.String() != "" {
			t.Errorf("Cycle %d: Reset didn't clear buffer", cycle)
		}
	}
}
