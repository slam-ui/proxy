//go:build windows
// +build windows

package killswitch

import (
	"strings"
	"sync"
	"testing"

	"proxyclient/internal/logger"
)

// ── IsEnabled Tests ───────────────────────────────────────────────────────────────

func TestIsEnabled_ReturnsFalse_Initially(t *testing.T) {
	// Reset state
	mu.Lock()
	enabled = false
	mu.Unlock()

	if IsEnabled() {
		t.Error("IsEnabled should return false initially")
	}
}

func TestIsEnabled_ReturnsTrue_AfterEnable(t *testing.T) {
	// Reset state first
	mu.Lock()
	enabled = false
	mu.Unlock()

	log := logger.New(logger.LevelInfo)

	// Note: Enable requires admin rights
	// This test verifies state management, not actual firewall rules
	Enable("127.0.0.1", log)

	// Cleanup
	defer func() {
		mu.Lock()
		enabled = false
		mu.Unlock()
	}()

	// Check state
	if !IsEnabled() {
		t.Error("IsEnabled should return true after Enable")
	}
}

// ── Enable Tests ──────────────────────────────────────────────────────────────────

func TestEnable_DoesNothing_WhenAlreadyEnabled(t *testing.T) {
	log := logger.New(logger.LevelInfo)

	// Set as enabled
	mu.Lock()
	enabled = true
	mu.Unlock()

	// Enable should be idempotent
	Enable("192.168.1.1", log)

	// Still enabled
	if !IsEnabled() {
		t.Error("Should still be enabled")
	}

	// Reset
	mu.Lock()
	enabled = false
	mu.Unlock()
}

func TestEnable_WithEmptyServerIP(t *testing.T) {
	log := logger.New(logger.LevelInfo)

	// Reset state
	mu.Lock()
	enabled = false
	mu.Unlock()

	// Enable with empty server IP
	Enable("", log)

	// Cleanup
	defer Disable(log)

	// Should still work
	if !IsEnabled() {
		t.Error("Enable should work with empty server IP")
	}
}

func TestEnable_WithLocalhostServerIP(t *testing.T) {
	log := logger.New(logger.LevelInfo)

	// Reset state
	mu.Lock()
	enabled = false
	mu.Unlock()

	// Enable with localhost
	Enable("127.0.0.1", log)

	// Cleanup
	defer Disable(log)

	if !IsEnabled() {
		t.Error("Enable should work with localhost server IP")
	}
}

// ── Disable Tests ─────────────────────────────────────────────────────────────────

func TestDisable_DoesNothing_WhenNotEnabled(t *testing.T) {
	log := logger.New(logger.LevelInfo)

	// Reset state
	mu.Lock()
	enabled = false
	mu.Unlock()

	// Disable should be idempotent
	Disable(log)

	// Still disabled
	if IsEnabled() {
		t.Error("Should still be disabled")
	}
}

func TestDisable_ResetsState(t *testing.T) {
	log := logger.New(logger.LevelInfo)

	// Set as enabled
	mu.Lock()
	enabled = true
	mu.Unlock()

	Disable(log)

	if IsEnabled() {
		t.Error("IsEnabled should return false after Disable")
	}
}

// ── CleanupOnStart Tests ──────────────────────────────────────────────────────────

func TestCleanupOnStart_ResetsState(t *testing.T) {
	log := logger.New(logger.LevelInfo)

	// Set as enabled
	mu.Lock()
	enabled = true
	mu.Unlock()

	CleanupOnStart(log)

	if IsEnabled() {
		t.Error("IsEnabled should return false after CleanupOnStart")
	}
}

func TestCleanupOnStart_SafeWhenNotEnabled(t *testing.T) {
	log := logger.New(logger.LevelInfo)

	// Reset state
	mu.Lock()
	enabled = false
	mu.Unlock()

	// Should not panic
	CleanupOnStart(log)
}

func TestCleanupOnStart_WithNilLogger(t *testing.T) {
	// Should not panic with nil logger
	CleanupOnStart(nil)
}

// ── Rule Name Tests ───────────────────────────────────────────────────────────────

func TestRuleNames(t *testing.T) {
	// Verify rule names are set correctly
	if ruleNameBlock == "" {
		t.Error("ruleNameBlock should not be empty")
	}
	if ruleNameAllow == "" {
		t.Error("ruleNameAllow should not be empty")
	}

	// Should be different
	if ruleNameBlock == ruleNameAllow {
		t.Error("ruleNameBlock and ruleNameAllow should be different")
	}

	// Should contain app identifier
	if !strings.Contains(ruleNameBlock, "ProxyClient") {
		t.Error("ruleNameBlock should contain ProxyClient")
	}
	if !strings.Contains(ruleNameAllow, "ProxyClient") {
		t.Error("ruleNameAllow should contain ProxyClient")
	}
}

// ── Concurrent Safety Tests ───────────────────────────────────────────────────────

func TestIsEnabled_Concurrent(t *testing.T) {
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			IsEnabled()
		}()
	}

	wg.Wait()
}

func TestEnableDisable_Concurrent(t *testing.T) {
	log := logger.New(logger.LevelInfo)

	// Reset state
	mu.Lock()
	enabled = false
	mu.Unlock()

	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			Enable("127.0.0.1", log)
		}()
		go func() {
			defer wg.Done()
			Disable(log)
		}()
	}

	wg.Wait()
	// Should not panic or race
}

// ── Enable/Disable Round Trip ─────────────────────────────────────────────────────

func TestEnableDisable_RoundTrip(t *testing.T) {
	log := logger.New(logger.LevelInfo)

	// Reset
	mu.Lock()
	enabled = false
	mu.Unlock()

	// Enable
	Enable("192.168.1.1", log)
	if !IsEnabled() {
		t.Error("Should be enabled after Enable")
	}

	// Disable
	Disable(log)
	if IsEnabled() {
		t.Error("Should be disabled after Disable")
	}

	// Enable again
	Enable("10.0.0.1", log)
	if !IsEnabled() {
		t.Error("Should be enabled after second Enable")
	}

	// Cleanup
	Disable(log)
}

// ── Edge Cases ────────────────────────────────────────────────────────────────────

func TestEnable_WithNilLogger(t *testing.T) {
	// Reset
	mu.Lock()
	enabled = false
	mu.Unlock()

	// Should not panic
	Enable("127.0.0.1", nil)

	// Cleanup
	Disable(nil)
}

func TestDisable_WithNilLogger(t *testing.T) {
	// Set enabled
	mu.Lock()
	enabled = true
	mu.Unlock()

	// Should not panic
	Disable(nil)
}

// ── State Management Tests ────────────────────────────────────────────────────────

func TestState_IsolatedBetweenTests(t *testing.T) {
	// This test verifies that state is properly reset between tests
	log := logger.New(logger.LevelInfo)

	// First ensure it's disabled
	Disable(log)

	if IsEnabled() {
		t.Error("Should start disabled")
	}
}

// ── ServerIP Handling Tests ───────────────────────────────────────────────────────

func TestEnable_ServerIPVariations(t *testing.T) {
	log := logger.New(logger.LevelInfo)

	testCases := []string{
		"",
		"127.0.0.1",
		"192.168.1.100",
		"10.0.0.1",
		"172.16.0.1",
		"8.8.8.8",
	}

	for _, ip := range testCases {
		t.Run("IP_"+ip, func(t *testing.T) {
			// Reset
			mu.Lock()
			enabled = false
			mu.Unlock()

			Enable(ip, log)

			if !IsEnabled() {
				t.Errorf("Enable should work with IP %q", ip)
			}

			Disable(log)
		})
	}
}

// ── Benchmark Tests ──────────────────────────────────────────────────────────────

func BenchmarkIsEnabled(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		IsEnabled()
	}
}

func BenchmarkEnableDisable(b *testing.B) {
	log := logger.New(logger.LevelInfo)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Enable("127.0.0.1", log)
		Disable(log)
	}
}

// ── Documentation Tests ───────────────────────────────────────────────────────────

func TestConstants_Documentation(t *testing.T) {
	t.Logf("ruleNameBlock = %q", ruleNameBlock)
	t.Logf("ruleNameAllow = %q", ruleNameAllow)

	// These serve as documentation
	if !strings.Contains(ruleNameBlock, "KillSwitch") {
		t.Log("Warning: ruleNameBlock doesn't contain 'KillSwitch'")
	}
	if !strings.Contains(ruleNameAllow, "KillSwitch") {
		t.Log("Warning: ruleNameAllow doesn't contain 'KillSwitch'")
	}
}

// ── Integration-style test ────────────────────────────────────────────────────────

func TestKillSwitch_FullLifecycle(t *testing.T) {
	log := logger.New(logger.LevelInfo)

	// Initial cleanup
	CleanupOnStart(log)

	// Should be disabled initially
	if IsEnabled() {
		t.Error("Should be disabled after CleanupOnStart")
	}

	// Enable with test server
	Enable("1.2.3.4", log)

	if !IsEnabled() {
		t.Error("Should be enabled after Enable")
	}

	// Disable
	Disable(log)

	if IsEnabled() {
		t.Error("Should be disabled after Disable")
	}

	// Final cleanup
	CleanupOnStart(log)
}

// ── Fuzz Tests ────────────────────────────────────────────────────────────────────

func FuzzEnable_ServerIP(f *testing.F) {
	seeds := []string{
		"127.0.0.1",
		"192.168.1.1",
		"",
		"invalid",
		"1.2.3.4",
		"::1",
	}

	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, serverIP string) {
		log := logger.New(logger.LevelInfo)

		// Reset
		mu.Lock()
		enabled = false
		mu.Unlock()

		// Should not panic with any input
		Enable(serverIP, log)
		Disable(log)
	})
}

// ── Table-driven tests ────────────────────────────────────────────────────────────

func TestRuleNames_Table(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		contains string
	}{
		{"block rule", ruleNameBlock, "Block"},
		{"allow rule", ruleNameAllow, "Allow"},
		{"block rule app", ruleNameBlock, "ProxyClient"},
		{"allow rule app", ruleNameAllow, "ProxyClient"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(tc.value, tc.contains) {
				t.Errorf("%s = %q, should contain %q", tc.name, tc.value, tc.contains)
			}
		})
	}
}

// ── State verification helper ─────────────────────────────────────────────────────

func TestStateHelper(t *testing.T) {
	// This test verifies that the enabled state is properly protected
	// by the mutex

	var wg sync.WaitGroup
	results := make([]bool, 100)

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = IsEnabled()
		}(i)
	}

	wg.Wait()

	// All results should be consistent
	first := results[0]
	for i, r := range results {
		if r != first {
			t.Errorf("Result %d = %v, want %v (race condition?)", i, r, first)
		}
	}
}
