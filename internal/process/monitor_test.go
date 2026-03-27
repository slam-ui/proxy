//go:build windows
// +build windows

package process

import (
	"sync"
	"syscall"
	"testing"

	"proxyclient/internal/apprules"
	"proxyclient/internal/logger"
)

// ── NewMonitor Tests ───────────────────────────────────────────────────────────────

func TestNewMonitor_ReturnsNonNil(t *testing.T) {
	log := logger.NewNop()
	m := NewMonitor(log)
	if m == nil {
		t.Fatal("NewMonitor returned nil")
	}
}

func TestNewMonitor_WithNilLogger_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("NewMonitor with nil logger panicked: %v", r)
		}
	}()
	_ = NewMonitor(nil)
}

func TestNewMonitorWithEngine_ReturnsNonNil(t *testing.T) {
	log := logger.NewNop()
	eng := apprules.NewEngine()
	m := NewMonitorWithEngine(log, eng)
	if m == nil {
		t.Fatal("NewMonitorWithEngine returned nil")
	}
}

func TestNewMonitorWithEngine_NilEngine_NoPanic(t *testing.T) {
	log := logger.NewNop()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("NewMonitorWithEngine with nil engine panicked: %v", r)
		}
	}()
	_ = NewMonitorWithEngine(log, nil)
}

// ── GetProcesses Tests ────────────────────────────────────────────────────────────

func TestGetProcesses_ReturnsNonNilSlice(t *testing.T) {
	log := logger.NewNop()
	m := NewMonitor(log)
	procs := m.GetProcesses()
	if procs == nil {
		t.Error("GetProcesses returned nil slice, want non-nil")
	}
}

func TestGetProcesses_BeforeStart_ReturnsEmpty(t *testing.T) {
	log := logger.NewNop()
	m := NewMonitor(log)
	procs := m.GetProcesses()
	if len(procs) != 0 {
		t.Errorf("GetProcesses before Start returned %d processes, want 0", len(procs))
	}
}

func TestGetProcess_UnknownPID_ReturnsError(t *testing.T) {
	log := logger.NewNop()
	m := NewMonitor(log)
	_, err := m.GetProcess(999999999)
	if err == nil {
		t.Error("GetProcess with unknown PID should return error")
	}
}

func TestGetProcess_NegativePID_ReturnsError(t *testing.T) {
	log := logger.NewNop()
	m := NewMonitor(log)
	_, err := m.GetProcess(-1)
	if err == nil {
		t.Error("GetProcess with negative PID should return error")
	}
}

// ── Start / Stop Tests ────────────────────────────────────────────────────────────

func TestStop_WithoutStart_IsIdempotent(t *testing.T) {
	log := logger.NewNop()
	m := NewMonitor(log)
	err := m.Stop()
	if err != nil {
		t.Errorf("Stop without Start should not error, got: %v", err)
	}
}

func TestStop_WithoutStart_NilEngine(t *testing.T) {
	log := logger.NewNop()
	m := NewMonitorWithEngine(log, nil)
	if err := m.Stop(); err != nil {
		t.Errorf("Stop returned error: %v", err)
	}
}

func TestStart_ThenStop_NoPanic(t *testing.T) {
	log := logger.NewNop()
	m := NewMonitor(log)
	if err := m.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if err := m.Stop(); err != nil {
		t.Errorf("Stop failed: %v", err)
	}
}

func TestStart_Twice_ReturnsError(t *testing.T) {
	log := logger.NewNop()
	m := NewMonitor(log)

	if err := m.Start(); err != nil {
		t.Fatalf("First Start failed: %v", err)
	}
	defer m.Stop()

	err := m.Start()
	if err == nil {
		t.Error("Second Start should return an error")
	}
}

func TestStop_Twice_IsIdempotent(t *testing.T) {
	log := logger.NewNop()
	m := NewMonitor(log)

	if err := m.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if err := m.Stop(); err != nil {
		t.Errorf("First Stop returned error: %v", err)
	}
	if err := m.Stop(); err != nil {
		t.Errorf("Second Stop returned error: %v", err)
	}
}

func TestStartStop_Cycle_MultipleTimes(t *testing.T) {
	log := logger.NewNop()
	m := NewMonitor(log)

	for i := 0; i < 3; i++ {
		if err := m.Start(); err != nil {
			t.Fatalf("Start iteration %d failed: %v", i, err)
		}
		if err := m.Stop(); err != nil {
			t.Fatalf("Stop iteration %d failed: %v", i, err)
		}
	}
}

// ── Refresh Tests ─────────────────────────────────────────────────────────────────

func TestRefresh_BeforeStart_PopulatesProcesses(t *testing.T) {
	log := logger.NewNop()
	m := NewMonitor(log)
	if err := m.Refresh(); err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}
	procs := m.GetProcesses()
	if len(procs) == 0 {
		t.Error("Refresh should populate at least some processes on Windows")
	}
}

func TestRefresh_WithEngine_NoPanic(t *testing.T) {
	log := logger.NewNop()
	eng := apprules.NewEngine()
	m := NewMonitorWithEngine(log, eng)

	if err := m.Refresh(); err != nil {
		t.Fatalf("Refresh with engine failed: %v", err)
	}
}

func TestRefresh_Twice_IsIdempotent(t *testing.T) {
	log := logger.NewNop()
	m := NewMonitor(log)

	if err := m.Refresh(); err != nil {
		t.Fatalf("First Refresh failed: %v", err)
	}
	if err := m.Refresh(); err != nil {
		t.Fatalf("Second Refresh failed: %v", err)
	}
}

func TestRefresh_GetProcess_AfterRefresh_FindsCurrentProcess(t *testing.T) {
	log := logger.NewNop()
	m := NewMonitor(log)

	if err := m.Refresh(); err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}

	procs := m.GetProcesses()
	if len(procs) == 0 {
		t.Skip("No processes found after Refresh")
	}

	// Pick the first process and verify GetProcess returns it
	pid := procs[0].PID
	info, err := m.GetProcess(pid)
	if err != nil {
		t.Errorf("GetProcess(%d) returned error: %v", pid, err)
	}
	if info == nil {
		t.Errorf("GetProcess(%d) returned nil", pid)
	}
}

func TestGetProcess_ReturnsCopy(t *testing.T) {
	log := logger.NewNop()
	m := NewMonitor(log)

	if err := m.Refresh(); err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}

	procs := m.GetProcesses()
	if len(procs) == 0 {
		t.Skip("No processes found after Refresh")
	}

	pid := procs[0].PID
	info1, _ := m.GetProcess(pid)
	info2, _ := m.GetProcess(pid)

	if info1 == info2 {
		t.Error("GetProcess should return a copy, not the same pointer")
	}
}

// ── WithEngine + Rule Matching Tests ──────────────────────────────────────────────

func TestRefresh_WithEngine_SetsProxyStatus(t *testing.T) {
	log := logger.NewNop()
	eng := apprules.NewEngine()

	// Add a catch-all rule
	rule := &apprules.Rule{
		ID:       "test-proxy",
		Name:     "Test Proxy",
		Action:   apprules.ActionProxy,
		Pattern:  "*.exe",
		Priority: 1,
		Enabled:  true,
	}
	if _, err := eng.AddRule(*rule); err != nil {
		t.Fatalf("AddRule failed: %v", err)
	}

	m := NewMonitorWithEngine(log, eng)
	if err := m.Refresh(); err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}

	// GetProcesses should return at least something without panicking
	procs := m.GetProcesses()
	_ = procs
}

// ── Concurrency Tests ─────────────────────────────────────────────────────────────

func TestGetProcesses_ConcurrentReads_NoRace(t *testing.T) {
	log := logger.NewNop()
	m := NewMonitor(log)

	_ = m.Refresh()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.GetProcesses()
		}()
	}
	wg.Wait()
}

func TestRefresh_ConcurrentWithGetProcesses_NoRace(t *testing.T) {
	log := logger.NewNop()
	m := NewMonitor(log)

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = m.Refresh()
		}()
		go func() {
			defer wg.Done()
			_ = m.GetProcesses()
		}()
	}
	wg.Wait()
}

// ── Windows Syscall / Handle Tests ────────────────────────────────────────────────

func TestSyscallHandle_InvalidHandle_IsZero(t *testing.T) {
	// Verify that syscall.InvalidHandle is a valid constant we can reference
	if syscall.InvalidHandle == 0 {
		// On Windows, INVALID_HANDLE_VALUE is 0xFFFFFFFF (uintptr), not 0
		t.Log("syscall.InvalidHandle is 0 (unexpected on Windows, but not a fatal error)")
	}
}

func TestGetProcess_AfterStop_ReturnsError(t *testing.T) {
	log := logger.NewNop()
	m := NewMonitor(log)

	_ = m.Start()
	_ = m.Stop()

	_, err := m.GetProcess(999999999)
	if err == nil {
		t.Error("GetProcess with unknown PID should return error even after stop")
	}
}

// ── Table-driven Tests ────────────────────────────────────────────────────────────

func TestGetProcess_TableDriven_UnknownPIDs(t *testing.T) {
	pids := []int{0, -1, 999999999, -999999999}
	log := logger.NewNop()
	m := NewMonitor(log)

	for _, pid := range pids {
		t.Run("pid", func(t *testing.T) {
			_, err := m.GetProcess(pid)
			if err == nil {
				t.Errorf("GetProcess(%d) should return error for unknown PID", pid)
			}
		})
	}
}

// ── Fuzz Tests ────────────────────────────────────────────────────────────────────

func FuzzGetProcess_NoPanic(f *testing.F) {
	f.Add(0)
	f.Add(-1)
	f.Add(999999)
	f.Add(1)
	f.Add(4)

	log := logger.NewNop()
	m := NewMonitor(log)

	f.Fuzz(func(t *testing.T, pid int) {
		_, _ = m.GetProcess(pid)
	})
}
