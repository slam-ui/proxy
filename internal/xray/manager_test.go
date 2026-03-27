package xray

import (
	"bytes"
	"strings"
	"testing"
)

// ─── tailWriter ───────────────────────────────────────────────────────────



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

// ─── IsTunConflict ─────────────────────────────────────────────────────────

func TestIsTunConflict_MatchesKnownSignatures(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   bool
	}{
		{
			name:   "Cannot create a file",
			output: "FATAL[0015] start service: start inbound/tun[tun-in]: configure tun interface: Cannot create a file when that file already exists.",
			want:   true,
		},
		{
			name:   "configure tun interface alone",
			output: "FATAL[0015] start inbound/tun[tun-in]: configure tun interface: some other error",
			want:   true,
		},
		{
			name:   "ERROR_GEN_FAILURE",
			output: "FATAL[0001] A device attached to the system is not functioning.",
			want:   true,
		},
		{
			name:   "normal startup error",
			output: "FATAL[0000] dial tcp: connection refused",
			want:   false,
		},
		{
			name:   "empty output",
			output: "",
			want:   false,
		},
		{
			name:   "unrelated WARN",
			output: "WARN[0010] inbound/tun[tun-in]: open interface take too much time to finish!",
			want:   false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := IsTunConflict(c.output)
			if got != c.want {
				t.Errorf("IsTunConflict(%q) = %v, want %v", c.output[:min(60, len(c.output))], got, c.want)
			}
		})
	}
}


