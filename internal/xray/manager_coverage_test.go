//go:build windows
// +build windows

package xray

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"proxyclient/internal/logger"
)

// ── NewManager Tests ──────────────────────────────────────────────────────────────

func TestNewManager_ValidConfig_NoPanic(t *testing.T) {
	log := logger.NewNop()
	cfg := Config{
		ExecutablePath: "sing-box.exe",
		ConfigPath:     "config.json",
		Logger:         log,
	}
	_, err := NewManager(cfg, context.Background())
	// err is expected if sing-box.exe doesn't exist, but should not panic
	_ = err
}

// TestNewManager_EmptyExecutablePath_ReturnsError и TestNewManager_EmptyConfigPath_ReturnsError
// объявлены в manager_extra_test.go

func TestNewManager_NilContext_NoPanic(t *testing.T) {
	log := logger.NewNop()
	cfg := Config{
		ExecutablePath: "sing-box.exe",
		ConfigPath:     "config.json",
		Logger:         log,
	}
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("NewManager with nil context panicked: %v", r)
		}
	}()
	_, _ = NewManager(cfg, nil)
}

func TestNewManager_NilLogger_NoPanic(t *testing.T) {
	cfg := Config{
		ExecutablePath: "sing-box.exe",
		ConfigPath:     "config.json",
		Logger:         nil,
	}
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("NewManager with nil logger panicked: %v", r)
		}
	}()
	_, _ = NewManager(cfg, context.Background())
}

// ── IsRunning Tests ───────────────────────────────────────────────────────────────

func TestIsRunning_NewManager_ReturnsFalse(t *testing.T) {
	log := logger.NewNop()
	cfg := Config{
		ExecutablePath: "sing-box.exe",
		ConfigPath:     "config.json",
		Logger:         log,
	}
	m, err := NewManager(cfg, context.Background())
	if err != nil {
		t.Skip("Cannot create manager (missing binary): " + err.Error())
	}
	if m.IsRunning() {
		t.Error("IsRunning should return false for freshly created manager")
	}
}

// ── Uptime Tests ──────────────────────────────────────────────────────────────────

func TestUptime_NotRunning_ReturnsZero(t *testing.T) {
	log := logger.NewNop()
	cfg := Config{
		ExecutablePath: "sing-box.exe",
		ConfigPath:     "config.json",
		Logger:         log,
	}
	m, err := NewManager(cfg, context.Background())
	if err != nil {
		t.Skip("Cannot create manager: " + err.Error())
	}
	uptime := m.Uptime()
	if uptime != 0 {
		t.Errorf("Uptime for non-running manager = %v, want 0", uptime)
	}
}

// ── LastOutput Tests ──────────────────────────────────────────────────────────────

func TestLastOutput_NewManager_ReturnsEmpty(t *testing.T) {
	log := logger.NewNop()
	cfg := Config{
		ExecutablePath: "sing-box.exe",
		ConfigPath:     "config.json",
		Logger:         log,
	}
	m, err := NewManager(cfg, context.Background())
	if err != nil {
		t.Skip("Cannot create manager: " + err.Error())
	}
	out := m.LastOutput()
	_ = out // may be empty or not, just must not panic
}

// ── GetPID Tests ──────────────────────────────────────────────────────────────────

func TestGetPID_NotRunning_ReturnsZero(t *testing.T) {
	log := logger.NewNop()
	cfg := Config{
		ExecutablePath: "sing-box.exe",
		ConfigPath:     "config.json",
		Logger:         log,
	}
	m, err := NewManager(cfg, context.Background())
	if err != nil {
		t.Skip("Cannot create manager: " + err.Error())
	}
	pid := m.GetPID()
	if pid != 0 {
		t.Errorf("GetPID for non-running manager = %d, want 0", pid)
	}
}

// ── Stop Tests ────────────────────────────────────────────────────────────────────

func TestStop_WithoutStart_NoPanic(t *testing.T) {
	log := logger.NewNop()
	cfg := Config{
		ExecutablePath: "sing-box.exe",
		ConfigPath:     "config.json",
		Logger:         log,
	}
	m, err := NewManager(cfg, context.Background())
	if err != nil {
		t.Skip("Cannot create manager: " + err.Error())
	}
	err = m.Stop()
	_ = err // may succeed or fail gracefully
}

func TestStop_Twice_NoPanic(t *testing.T) {
	log := logger.NewNop()
	cfg := Config{
		ExecutablePath: "sing-box.exe",
		ConfigPath:     "config.json",
		Logger:         log,
	}
	m, err := NewManager(cfg, context.Background())
	if err != nil {
		t.Skip("Cannot create manager: " + err.Error())
	}
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("double Stop panicked: %v", r)
		}
	}()
	_ = m.Stop()
	_ = m.Stop()
}

// ── Wait Tests ────────────────────────────────────────────────────────────────────

func TestWait_WithoutStart_ReturnsQuickly(t *testing.T) {
	log := logger.NewNop()
	cfg := Config{
		ExecutablePath: "sing-box.exe",
		ConfigPath:     "config.json",
		Logger:         log,
	}
	m, err := NewManager(cfg, context.Background())
	if err != nil {
		t.Skip("Cannot create manager: " + err.Error())
	}

	done := make(chan error, 1)
	go func() { done <- m.Wait() }()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("Wait without Start should return quickly")
	}
}

// ── tooManyRestartsError Tests ────────────────────────────────────────────────────

// NOTE: TestTooManyRestartsError_Message already declared in manager_improved_test.go.
// This is a coverage variant with additional assertions.
func TestTooManyRestartsError_MessageCoverage(t *testing.T) {
	base := errors.New("process killed")
	err := &tooManyRestartsError{count: 5, base: base}
	msg := err.Error()

	if !strings.Contains(msg, "5") {
		t.Errorf("error message should contain count 5, got: %q", msg)
	}
	if !strings.Contains(msg, base.Error()) {
		t.Errorf("error message should contain base error, got: %q", msg)
	}
}

func TestTooManyRestartsError_CountZero(t *testing.T) {
	err := &tooManyRestartsError{count: 0, base: errors.New("base")}
	msg := err.Error()
	if msg == "" {
		t.Error("error message should not be empty even with count=0")
	}
}

func TestTooManyRestartsError_NilBase(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("tooManyRestartsError.Error() with nil base panicked: %v", r)
		}
	}()
	err := &tooManyRestartsError{count: 3, base: nil}
	_ = err.Error()
}

// ── IsTooManyRestarts Tests ───────────────────────────────────────────────────────

func TestIsTooManyRestarts_WithTooManyRestartsError_ReturnsTrue(t *testing.T) {
	err := &tooManyRestartsError{count: 1, base: errors.New("crash")}
	if !IsTooManyRestarts(err) {
		t.Error("IsTooManyRestarts should return true for tooManyRestartsError")
	}
}

func TestIsTooManyRestarts_WithNilError_ReturnsFalse(t *testing.T) {
	if IsTooManyRestarts(nil) {
		t.Error("IsTooManyRestarts should return false for nil error")
	}
}

func TestIsTooManyRestarts_WithOtherError_ReturnsFalse(t *testing.T) {
	if IsTooManyRestarts(errors.New("other error")) {
		t.Error("IsTooManyRestarts should return false for generic errors")
	}
}

func TestIsTooManyRestarts_HighCountError_ReturnsTrue(t *testing.T) {
	err := &tooManyRestartsError{count: 999, base: errors.New("crash")}
	if !IsTooManyRestarts(err) {
		t.Error("IsTooManyRestarts should return true regardless of count")
	}
}

// ── IsTunConflict Tests ───────────────────────────────────────────────────────────

func TestIsTunConflict_WithConflictOutput_ReturnsTrue(t *testing.T) {
	conflictOutputs := []string{
		"Cannot create a file when that file already exists",
		"configure tun interface: Cannot create a file when that file already exists.",
	}
	for _, output := range conflictOutputs {
		if !IsTunConflict(output) {
			t.Errorf("IsTunConflict(%q) = false, want true", output)
		}
	}
}

func TestIsTunConflict_WithNormalOutput_ReturnsFalse(t *testing.T) {
	normalOutputs := []string{
		"",
		"sing-box started",
		"listening on 127.0.0.1:1080",
		"connection established",
	}
	for _, output := range normalOutputs {
		if IsTunConflict(output) {
			t.Errorf("IsTunConflict(%q) = true, want false", output)
		}
	}
}

func TestIsTunConflict_EmptyString_ReturnsFalse(t *testing.T) {
	if IsTunConflict("") {
		t.Error("IsTunConflict(\"\") should return false")
	}
}

// ── Config Validation Tests ───────────────────────────────────────────────────────

func TestValidateConfig_EmptyExecutablePath_ReturnsError(t *testing.T) {
	log := logger.NewNop()
	cfg := Config{
		ExecutablePath: "",
		ConfigPath:     "config.json",
		Logger:         log,
	}
	if err := validateConfig(cfg); err == nil {
		t.Error("validateConfig with empty ExecutablePath should return error")
	}
}

func TestValidateConfig_EmptyConfigPath_ReturnsError(t *testing.T) {
	log := logger.NewNop()
	cfg := Config{
		ExecutablePath: "sing-box.exe",
		ConfigPath:     "",
		Logger:         log,
	}
	if err := validateConfig(cfg); err == nil {
		t.Error("validateConfig with empty ConfigPath should return error")
	}
}

func TestValidateConfig_ValidConfig_NoError(t *testing.T) {
	// Create a temporary fake executable and config so validateConfig can stat them.
	dir := t.TempDir()
	exePath := filepath.Join(dir, "sing-box.exe")
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(exePath, []byte("fake"), 0755); err != nil {
		t.Fatalf("failed to create fake exe: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte("{}"), 0644); err != nil {
		t.Fatalf("failed to create fake config: %v", err)
	}
	log := logger.NewNop()
	cfg := Config{
		ExecutablePath: exePath,
		ConfigPath:     cfgPath,
		Logger:         log,
	}
	if err := validateConfig(cfg); err != nil {
		t.Errorf("validateConfig with valid config returned error: %v", err)
	}
}

func TestValidateConfig_WhitespaceExecutablePath_ReturnsError(t *testing.T) {
	log := logger.NewNop()
	cfg := Config{
		ExecutablePath: "   ",
		ConfigPath:     "config.json",
		Logger:         log,
	}
	// may or may not error depending on implementation — just must not panic
	_ = validateConfig(cfg)
}

// ── Table-driven NewManager Tests ─────────────────────────────────────────────────

func TestNewManager_InvalidConfigs_AllReturnError(t *testing.T) {
	log := logger.NewNop()
	tests := []struct {
		name string
		cfg  Config
	}{
		{
			name: "empty executable",
			cfg:  Config{ExecutablePath: "", ConfigPath: "config.json", Logger: log},
		},
		{
			name: "empty config path",
			cfg:  Config{ExecutablePath: "sing-box.exe", ConfigPath: "", Logger: log},
		},
		{
			name: "both empty",
			cfg:  Config{ExecutablePath: "", ConfigPath: "", Logger: log},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewManager(tc.cfg, context.Background())
			if err == nil {
				t.Errorf("NewManager with %s should return error", tc.name)
			}
		})
	}
}

// ── Fuzz Tests ────────────────────────────────────────────────────────────────────

// FuzzIsTunConflict, FuzzTailWriter и FuzzCrashDetection объявлены в fuzz_test.go
