package xray

import (
	"bytes"
	"strings"
	"testing"
)

// ─── tailWriter ───────────────────────────────────────────────────────────

func TestTailWriter_CapturesOutput(t *testing.T) {
	tw := newTailWriter(1024)
	tw.Write([]byte("hello world\n"))
	if !strings.Contains(tw.String(), "hello world") {
		t.Error("tailWriter did not capture written bytes")
	}
}

func TestTailWriter_TruncatesOldData(t *testing.T) {
	// max=10, write 20 bytes of A then 8 bytes of B
	// after second write: buf = last 10 bytes = "AABBBBBBBB"
	// After line-boundary trim (no \n in A block): still last 10
	// To guarantee ALL A's are dropped: write enough B's to exceed max alone
	tw := newTailWriter(8)
	tw.Write([]byte("AAAAAAAAAAAAAAAA")) // 16 A's — fills and wraps
	tw.Write([]byte("BBBBBBBB"))         // exactly 8 B's = max → only B's remain
	s := tw.String()
	if strings.Contains(s, "A") {
		t.Errorf("tailWriter should have dropped old data, got: %q", s)
	}
	if !strings.Contains(s, "BBBB") {
		t.Error("tailWriter should keep newest data")
	}
}

func TestTailWriter_DoesNotCutMidLine(t *testing.T) {
	// Write lines that together exceed the buffer.
	// After truncation the first line in buf must be complete (start after \n).
	tw := newTailWriter(40)
	// Write 3 complete lines totalling > 40 bytes
	tw.Write([]byte("line1_padding_12345\n")) // 20 bytes
	tw.Write([]byte("line2_padding_12345\n")) // 20 bytes → total 40, exact
	tw.Write([]byte("line3_short\n"))         // 12 bytes → truncation triggers

	s := tw.String()
	// After truncation result must start at a line boundary, not mid-word
	if len(s) > 0 && s[0] != 'l' {
		// OK if it starts on 'l' of line2 or line3
		t.Errorf("tailWriter buf starts mid-line: %q", s[:min(20, len(s))])
	}
	if !strings.Contains(s, "line3_short") {
		t.Error("newest line must be retained")
	}
}

func TestTailWriter_CrashMessageNotCut(t *testing.T) {
	// Simulate wintun crash message at buffer boundary.
	// The crash detector looks for "Cannot create a file when that file already exists".
	tw := newTailWriter(64)
	padding := bytes.Repeat([]byte("x"), 30)
	tw.Write(padding)
	tw.Write([]byte("\nFATAL[0015] Cannot create a file when that file already exists\n"))

	if !strings.Contains(tw.String(), "Cannot create a file") {
		t.Error("crash message should be fully retained in tail buffer")
	}
}

func TestTailWriter_ConcurrentWrites(t *testing.T) {
	tw := newTailWriter(512)
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 20; j++ {
				tw.Write([]byte("data\n"))
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
	// Must not panic and result must be valid UTF-8 / non-empty
	if tw.String() == "" {
		t.Error("tailWriter should have data after concurrent writes")
	}
}

// ─── crashTracker ─────────────────────────────────────────────────────────

func TestCrashTracker_CountsConsecutiveCrashes(t *testing.T) {
	ct := &crashTracker{}
	for i := 1; i <= maxCrashCount; i++ {
		n := ct.Record()
		if n != i {
			t.Errorf("Record() = %d, want %d", n, i)
		}
	}
}

func TestCrashTracker_ResetClearsCount(t *testing.T) {
	ct := &crashTracker{}
	ct.Record()
	ct.Record()
	ct.Reset()
	if n := ct.Record(); n != 1 {
		t.Errorf("after Reset, first Record() = %d, want 1", n)
	}
}

func TestCrashTracker_OldCrashesExpire(t *testing.T) {
	ct := &crashTracker{}
	// Simulate last crash was long ago by directly setting lastCrashAt
	ct.mu.Lock()
	ct.count = maxCrashCount - 1
	ct.lastCrashAt = ct.lastCrashAt.Add(-crashRateWindow * 2) // way in the past
	ct.mu.Unlock()

	// Next Record should reset the window and start fresh at 1
	n := ct.Record()
	if n != 1 {
		t.Errorf("crash window expired, expected fresh count=1, got %d", n)
	}
}

func TestIsTooManyRestarts_TypeCheck(t *testing.T) {
	err := &tooManyRestartsError{count: 3}
	if !IsTooManyRestarts(err) {
		t.Error("IsTooManyRestarts should return true for tooManyRestartsError")
	}
	if IsTooManyRestarts(nil) {
		t.Error("IsTooManyRestarts should return false for nil")
	}
}

// helpers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
