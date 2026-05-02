package xray

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"proxyclient/internal/logger"
)

// ─── tailWriter ───────────────────────────────────────────────────────────

func TestTailWriter_CrashMessageNotCut(t *testing.T) {
	// Simulate wintun crash message at buffer boundary.
	// The crash detector looks for "Cannot create a file when that file already exists".
	tw := newTailWriter(64)
	padding := bytes.Repeat([]byte("x"), 30)
	mustWriteTail(t, tw, padding)
	mustWriteTail(t, tw, []byte("\nFATAL[0015] Cannot create a file when that file already exists\n"))

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

func TestManagerStartFailureRestoresDone(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"log":{"level":"info"}}`), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	mgr, err := NewManager(Config{
		ExecutablePath: os.Args[0],
		ConfigPath:     configPath,
		Args:           []string{"-test.run=TestXrayHelperProcess", "--", "--xray-helper-exit"},
		Logger:         logger.NewNop(),
	}, context.Background())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	m := mgr.(*manager)
	if err := m.Wait(); err != nil {
		t.Fatalf("Wait initial helper process: %v", err)
	}

	restartErr := errors.New("restart cleanup failed")
	m.mu.Lock()
	m.config.BeforeRestart = func(context.Context, logger.Logger) error {
		return restartErr
	}
	m.mu.Unlock()

	if err := m.Start(); !errors.Is(err, restartErr) {
		t.Fatalf("Start error = %v, want %v", err, restartErr)
	}
	if m.IsRunning() {
		t.Fatal("failed restart must not leave manager marked running")
	}

	done := make(chan error, 1)
	go func() { done <- m.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Wait after failed restart returned %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Wait blocked after failed restart")
	}
}

func TestXrayHelperProcess(t *testing.T) {
	for _, arg := range os.Args {
		if arg == "--xray-helper-exit" {
			os.Exit(0)
		}
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
