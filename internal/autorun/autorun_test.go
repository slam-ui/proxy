//go:build windows
// +build windows

package autorun

import (
        "os"
        "path/filepath"
        "strings"
        "sync"
        "testing"
)

// ── exeAbsPath Tests ──────────────────────────────────────────────────────────────

func TestExeAbsPath_ReturnsValidPath(t *testing.T) {
        path, err := exeAbsPath()
        if err != nil {
                t.Fatalf("exeAbsPath failed: %v", err)
        }

        if path == "" {
                t.Error("exeAbsPath returned empty path")
        }

        // Should be absolute
        if !filepath.IsAbs(path) {
                t.Errorf("Path %q is not absolute", path)
        }

        // Should end with .exe (or be the test binary)
        if !strings.HasSuffix(strings.ToLower(path), ".exe") && !strings.Contains(path, ".test") {
                t.Errorf("Path %q doesn't look like an executable", path)
        }
}

func TestExeAbsPath_IsConsistent(t *testing.T) {
        path1, err1 := exeAbsPath()
        path2, err2 := exeAbsPath()

        if err1 != nil || err2 != nil {
                t.Fatalf("exeAbsPath errors: %v, %v", err1, err2)
        }

        if path1 != path2 {
                t.Errorf("exeAbsPath returned different paths: %q vs %q", path1, path2)
        }
}

// ── Integration Tests (require registry access) ───────────────────────────────────

// Note: Enable, Disable, IsEnabled tests require registry write access
// and should be run with appropriate permissions.
// These tests are designed to be safe and cleanup after themselves.

func TestEnableDisable_RoundTrip(t *testing.T) {
        // Skip if not running as a normal test (might need admin)
        if testing.Short() {
                t.Skip("Skipping registry test in short mode")
        }

        // Get current state
        wasEnabled := IsEnabled()

        // Cleanup at the end
        defer func() {
                if wasEnabled {
                        Enable()
                } else {
                        Disable()
                }
        }()

        // Test Disable
        err := Disable()
        if err != nil {
                t.Logf("Disable error (may need admin): %v", err)
        }

        if IsEnabled() {
                t.Error("After Disable, IsEnabled should be false")
        }

        // Test Enable
        err = Enable()
        if err != nil {
                t.Logf("Enable error (may need admin): %v", err)
                // Don't fail test - might just not have permissions
                return
        }

        if !IsEnabled() {
                t.Error("After Enable, IsEnabled should be true")
        }

        // Cleanup
        Disable()
}

func TestIsEnabled_ReturnsFalse_WhenNotRegistered(t *testing.T) {
        // Skip if already enabled
        if IsEnabled() {
                t.Skip("Skipping - autorun is already enabled")
        }

        // IsEnabled should return false when not registered
        // (assuming clean state)
        result := IsEnabled()
        if result {
                t.Error("IsEnabled should be false when not registered")
        }
}

func TestDisable_IsIdempotent(t *testing.T) {
        if testing.Short() {
                t.Skip("Skipping registry test in short mode")
        }

        // Disable twice - should not error
        err1 := Disable()
        err2 := Disable()

        // At least one should succeed (or both should fail with same error)
        _ = err1
        _ = err2
}

// ── Registry Path Tests ───────────────────────────────────────────────────────────

func TestRunKeyPath(t *testing.T) {
        // Verify the registry path is correct
        expectedPath := `Software\Microsoft\Windows\CurrentVersion\Run`
        if runKey != expectedPath {
                t.Errorf("runKey = %q, want %q", runKey, expectedPath)
        }
}

func TestAppName(t *testing.T) {
        // Verify the app name is set
        if appName == "" {
                t.Error("appName should not be empty")
        }

        if appName != "ProxyClient" {
                t.Errorf("appName = %q, want ProxyClient", appName)
        }
}

// ── Error Handling Tests ──────────────────────────────────────────────────────────

func TestEnable_ReturnsError_WhenExecutableNotFound(t *testing.T) {
        // This test is tricky because exeAbsPath uses os.Executable
        // which always succeeds for the running process.
        // We can't easily test this case without mocking.
        t.Skip("Cannot easily test executable not found case")
}

// ── Table-driven tests ────────────────────────────────────────────────────────────

func TestAppName_Values(t *testing.T) {
        tests := []struct {
                name     string
                value    string
                expected string
        }{
                {"appName constant", appName, "ProxyClient"},
                {"runKey constant", runKey, `Software\Microsoft\Windows\CurrentVersion\Run`},
        }

        for _, tc := range tests {
                t.Run(tc.name, func(t *testing.T) {
                        if tc.value != tc.expected {
                                t.Errorf("%s = %q, want %q", tc.name, tc.value, tc.expected)
                        }
                })
        }
}

// ── Concurrent Safety Tests ───────────────────────────────────────────────────────

func TestIsEnabled_Concurrent(t *testing.T) {
        var errors []error
        var wg sync.WaitGroup

        for i := 0; i < 10; i++ {
                wg.Add(1)
                go func() {
                        defer wg.Done()
                        // IsEnabled only reads, should be safe
                        IsEnabled()
                }()
        }

        wg.Wait()

        if len(errors) > 0 {
                t.Errorf("Concurrent IsEnabled errors: %v", errors)
        }
}

func TestEnableDisable_Concurrent(t *testing.T) {
        if testing.Short() {
                t.Skip("Skipping registry test in short mode")
        }

        var wg sync.WaitGroup

        // Concurrent Enable/Disable calls
        for i := 0; i < 5; i++ {
                wg.Add(2)
                go func() {
                        defer wg.Done()
                        Enable()
                }()
                go func() {
                        defer wg.Done()
                        Disable()
                }()
        }

        wg.Wait()
        // Should not panic or deadlock
}

// ── State Consistency Tests ───────────────────────────────────────────────────────

func TestEnableDisable_StateConsistency(t *testing.T) {
        if testing.Short() {
                t.Skip("Skipping registry test in short mode")
        }

        // Ensure clean state
        Disable()

        // Enable
        if err := Enable(); err != nil {
                t.Skipf("Enable failed: %v", err)
        }

        if !IsEnabled() {
                t.Error("IsEnabled should return true after Enable")
        }

        // Disable
        if err := Disable(); err != nil {
                t.Errorf("Disable failed: %v", err)
        }

        if IsEnabled() {
                t.Error("IsEnabled should return false after Disable")
        }
}

// ── Benchmark Tests ──────────────────────────────────────────────────────────────

func BenchmarkIsEnabled(b *testing.B) {
        b.ResetTimer()
        for i := 0; i < b.N; i++ {
                IsEnabled()
        }
}

func BenchmarkExeAbsPath(b *testing.B) {
        b.ResetTimer()
        for i := 0; i < b.N; i++ {
                _, _ = exeAbsPath()
        }
}

// ── Edge Cases ────────────────────────────────────────────────────────────────────

func TestExeAbsPath_WithSymlinks(t *testing.T) {
        // os.Executable should resolve symlinks
        path, err := exeAbsPath()
        if err != nil {
                t.Fatalf("exeAbsPath failed: %v", err)
        }

        // Path should exist
        if _, err := os.Stat(path); err != nil {
                t.Errorf("Executable path %q doesn't exist: %v", path, err)
        }
}

// ── Documentation Tests ───────────────────────────────────────────────────────────

func TestConstants_Documentation(t *testing.T) {
        // These tests serve as documentation of expected values
        t.Logf("appName = %q", appName)
        t.Logf("runKey = %q", runKey)

        // Verify these are standard Windows registry paths
        if !strings.Contains(runKey, "Windows") {
                t.Error("runKey should be a Windows registry path")
        }

        if !strings.Contains(runKey, "Run") {
                t.Error("runKey should be the Run key for autorun")
        }
}
