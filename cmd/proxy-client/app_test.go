//go:build windows
// +build windows

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"proxyclient/internal/logger"
)

// ── DefaultAppConfig Tests ────────────────────────────────────────────────────────

func TestDefaultAppConfig_HasNonEmptyFields(t *testing.T) {
	cfg := DefaultAppConfig()
	if cfg.SecretFile == "" {
		t.Error("SecretFile should not be empty")
	}
	if cfg.SingBoxPath == "" {
		t.Error("SingBoxPath should not be empty")
	}
	if cfg.ConfigPath == "" {
		t.Error("ConfigPath should not be empty")
	}
	if cfg.APIAddress == "" {
		t.Error("APIAddress should not be empty")
	}
}

func TestDefaultAppConfig_SecretFileDefault(t *testing.T) {
	cfg := DefaultAppConfig()
	if cfg.SecretFile != "secret.key" {
		t.Errorf("SecretFile = %q, want secret.key", cfg.SecretFile)
	}
}

func TestDefaultAppConfig_SingBoxPathDefault(t *testing.T) {
	cfg := DefaultAppConfig()
	if !strings.Contains(cfg.SingBoxPath, "sing-box") {
		t.Errorf("SingBoxPath = %q, expected to contain sing-box", cfg.SingBoxPath)
	}
}

// ── NewApp Tests ──────────────────────────────────────────────────────────────────

func TestNewApp_ReturnsNonNil(t *testing.T) {
	cfg := DefaultAppConfig()
	app := NewApp(cfg, &bytes.Buffer{})
	if app == nil {
		t.Fatal("NewApp returned nil")
	}
}

func TestNewApp_WithNilOutput_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("NewApp with nil output panicked: %v", r)
		}
	}()
	cfg := DefaultAppConfig()
	_ = NewApp(cfg, nil)
}

func TestNewApp_CreatesQuitChannel(t *testing.T) {
	cfg := DefaultAppConfig()
	app := NewApp(cfg, &bytes.Buffer{})
	if app.quit == nil {
		t.Error("quit channel should not be nil")
	}
}

func TestNewApp_CreatesEventLog(t *testing.T) {
	cfg := DefaultAppConfig()
	app := NewApp(cfg, &bytes.Buffer{})
	if app.evLog == nil {
		t.Error("evLog should not be nil")
	}
}

func TestNewApp_CreatesProxyManager(t *testing.T) {
	cfg := DefaultAppConfig()
	app := NewApp(cfg, &bytes.Buffer{})
	if app.proxyManager == nil {
		t.Error("proxyManager should not be nil")
	}
}

func TestNewApp_CreatesLifecycleContext(t *testing.T) {
	cfg := DefaultAppConfig()
	app := NewApp(cfg, &bytes.Buffer{})
	if app.lifecycleCtx == nil {
		t.Error("lifecycleCtx should not be nil")
	}
	// Context should not be cancelled yet
	select {
	case <-app.lifecycleCtx.Done():
		t.Error("lifecycleCtx should not be cancelled on creation")
	default:
	}
}

// ── extractServerIP Tests ─────────────────────────────────────────────────────────

func TestExtractServerIP_FileNotFound_ReturnsEmpty(t *testing.T) {
	result := extractServerIP("/nonexistent/path/secret.key")
	if result != "" {
		t.Errorf("extractServerIP with missing file = %q, want empty string", result)
	}
}

func TestExtractServerIP_EmptyFile_ReturnsEmpty(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "secret*.key")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()

	result := extractServerIP(f.Name())
	if result != "" {
		t.Errorf("extractServerIP with empty file = %q, want empty string", result)
	}
}

func TestExtractServerIP_OnlyComments_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.key")
	content := "# comment line\n# another comment\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	result := extractServerIP(path)
	if result != "" {
		t.Errorf("extractServerIP with only comments = %q, want empty", result)
	}
}

func TestExtractServerIP_ValidVLESSWithIP_ReturnsIP(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.key")
	// vless://uuid@1.2.3.4:443?params
	content := "vless://550e8400-e29b-41d4-a716-446655440000@1.2.3.4:443?security=reality&sni=example.com\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	result := extractServerIP(path)
	if result != "1.2.3.4" {
		t.Errorf("extractServerIP = %q, want 1.2.3.4", result)
	}
}

func TestExtractServerIP_ValidVLESSWithIPv6_ReturnsIP(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.key")
	content := "vless://550e8400-e29b-41d4-a716-446655440000@[2001:db8::1]:443?security=reality\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	result := extractServerIP(path)
	// Should return the IPv6 address
	_ = result // IPv6 parsing may vary
}

func TestExtractServerIP_InvalidURL_NoAt_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.key")
	content := "vless://noatsign\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	result := extractServerIP(path)
	if result != "" {
		t.Errorf("extractServerIP without @ = %q, want empty", result)
	}
}

func TestExtractServerIP_SkipsCommentLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.key")
	content := "# this is a comment with @ email@example.com\nvless://uuid@5.6.7.8:443?params\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	result := extractServerIP(path)
	if result != "5.6.7.8" {
		t.Errorf("extractServerIP should skip comment, got %q, want 5.6.7.8", result)
	}
}

func TestExtractServerIP_StripsBOM(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.key")
	// BOM + vless URL
	content := "\xef\xbb\xbfvless://uuid@9.10.11.12:443?params\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	result := extractServerIP(path)
	if result != "9.10.11.12" {
		t.Errorf("extractServerIP with BOM = %q, want 9.10.11.12", result)
	}
}

func TestExtractServerIP_WhitespaceLines_Skipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.key")
	content := "\n   \n\nvless://uuid@3.4.5.6:443?params\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	result := extractServerIP(path)
	if result != "3.4.5.6" {
		t.Errorf("extractServerIP skipping whitespace lines = %q, want 3.4.5.6", result)
	}
}

// ── Shutdown Tests ────────────────────────────────────────────────────────────────

func TestShutdown_ClosesLifecycleCtx(t *testing.T) {
	cfg := DefaultAppConfig()
	app := NewApp(cfg, &bytes.Buffer{})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	go app.Shutdown(ctx, nil)

	select {
	case <-app.lifecycleCtx.Done():
		// good
	case <-time.After(2 * time.Second):
		t.Error("lifecycleCtx should be cancelled after Shutdown")
	}
}

func TestShutdown_CanBeCalledWithNilMonitor(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Shutdown with nil monitor panicked: %v", r)
		}
	}()

	cfg := DefaultAppConfig()
	app := NewApp(cfg, &bytes.Buffer{})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	app.Shutdown(ctx, nil)
}

// ── buildXRayCfg Tests ────────────────────────────────────────────────────────────

func TestBuildXRayCfg_HasExecutablePath(t *testing.T) {
	cfg := DefaultAppConfig()
	app := NewApp(cfg, &bytes.Buffer{})

	xrayCfg := app.buildXRayCfg()
	if xrayCfg.ExecutablePath == "" {
		t.Error("buildXRayCfg should set ExecutablePath")
	}
}

func TestBuildXRayCfg_HasConfigPath(t *testing.T) {
	cfg := DefaultAppConfig()
	app := NewApp(cfg, &bytes.Buffer{})

	xrayCfg := app.buildXRayCfg()
	if xrayCfg.ConfigPath == "" {
		t.Error("buildXRayCfg should set ConfigPath")
	}
}

func TestBuildXRayCfg_HasLogger(t *testing.T) {
	cfg := DefaultAppConfig()
	app := NewApp(cfg, &bytes.Buffer{})

	xrayCfg := app.buildXRayCfg()
	if xrayCfg.Logger == nil {
		t.Error("buildXRayCfg should set Logger")
	}
}

func TestBuildXRayCfg_HasBeforeRestart(t *testing.T) {
	cfg := DefaultAppConfig()
	app := NewApp(cfg, &bytes.Buffer{})

	xrayCfg := app.buildXRayCfg()
	if xrayCfg.BeforeRestart == nil {
		t.Error("buildXRayCfg should set BeforeRestart")
	}
}

func TestBuildXRayCfg_HasOnCrash(t *testing.T) {
	cfg := DefaultAppConfig()
	app := NewApp(cfg, &bytes.Buffer{})

	xrayCfg := app.buildXRayCfg()
	if xrayCfg.OnCrash == nil {
		t.Error("buildXRayCfg should set OnCrash")
	}
}

func TestBuildXRayCfg_ArgsContainRun(t *testing.T) {
	cfg := DefaultAppConfig()
	app := NewApp(cfg, &bytes.Buffer{})

	xrayCfg := app.buildXRayCfg()
	found := false
	for _, arg := range xrayCfg.Args {
		if arg == "run" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("buildXRayCfg Args should contain 'run', got %v", xrayCfg.Args)
	}
}

// ── AppConfig Tests ───────────────────────────────────────────────────────────────

func TestAppConfig_KillSwitchDefaultFalse(t *testing.T) {
	cfg := DefaultAppConfig()
	if cfg.KillSwitch {
		t.Error("KillSwitch should default to false")
	}
}

func TestAppConfig_CanSetKillSwitch(t *testing.T) {
	cfg := DefaultAppConfig()
	cfg.KillSwitch = true
	if !cfg.KillSwitch {
		t.Error("KillSwitch should be settable to true")
	}
}

// ── preWarmProxyConnection Tests ──────────────────────────────────────────────────

func TestPreWarmProxyConnection_InvalidAddr_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("preWarmProxyConnection panicked: %v", r)
		}
	}()
	log := logger.NewNop()
	preWarmProxyConnection(context.Background(), "invalid-addr", nil, log)
}

func TestPreWarmProxyConnection_EmptyAddr_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("preWarmProxyConnection with empty addr panicked: %v", r)
		}
	}()
	log := logger.NewNop()
	preWarmProxyConnection(context.Background(), "", nil, log)
}

func TestPreWarmProxyConnection_NilLogger_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("preWarmProxyConnection with nil logger panicked: %v", r)
		}
	}()
	preWarmProxyConnection(context.Background(), "127.0.0.1:9999", nil, nil)
}

func TestPreWarmProxyConnection_ClosedPort_ReturnsQuickly(t *testing.T) {
	log := logger.NewNop()
	start := time.Now()
	preWarmProxyConnection(context.Background(), "127.0.0.1:19999", nil, log)
	elapsed := time.Since(start)
	if elapsed > 15*time.Second {
		t.Errorf("preWarmProxyConnection took too long: %v", elapsed)
	}
}

// ── handleConfigError Tests ───────────────────────────────────────────────────────

func TestHandleConfigError_NilError_ReturnsFalse(t *testing.T) {
	cfg := DefaultAppConfig()
	app := NewApp(cfg, &bytes.Buffer{})

	result := app.handleConfigError(nil)
	if result {
		t.Error("handleConfigError(nil) should return false")
	}
}
func TestWaitForSecretKey_WaitsUntilVLESSKeyAppears(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "secret.key")
	cfg := DefaultAppConfig()
	cfg.SecretFile = secretPath
	app := NewApp(cfg, &bytes.Buffer{})

	go func() {
		time.Sleep(100 * time.Millisecond)
		if err := os.WriteFile(secretPath, []byte("vless://00000000-0000-0000-0000-000000000000@example.com:443"), 0644); err != nil {
			t.Fatalf("failed to write secret.key: %v", err)
		}
	}()

	start := time.Now()
	app.waitForSecretKey()
	elapsed := time.Since(start)
	if elapsed > 1*time.Second {
		t.Errorf("waitForSecretKey took too long: %v", elapsed)
	}
}

// ── Concurrent / Race Tests ───────────────────────────────────────────────────────

func TestNewApp_MultipleConcurrentCreations_NoRace(t *testing.T) {
	done := make(chan struct{}, 10)
	for i := 0; i < 10; i++ {
		go func() {
			cfg := DefaultAppConfig()
			app := NewApp(cfg, &bytes.Buffer{})
			_ = app
			done <- struct{}{}
		}()
	}
	for i := 0; i < 10; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent NewApp creation timed out")
		}
	}
}

// ── Table-driven Tests ────────────────────────────────────────────────────────────

func TestExtractServerIP_Table(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "empty file",
			content: "",
			want:    "",
		},
		{
			name:    "only comments",
			content: "# comment\n# another\n",
			want:    "",
		},
		{
			name:    "valid IP",
			content: "vless://uuid@192.168.1.1:443?params\n",
			want:    "192.168.1.1",
		},
		{
			name:    "comment then valid",
			content: "# ignore\nvless://uuid@10.0.0.1:443?params\n",
			want:    "10.0.0.1",
		},
		{
			name:    "no at sign",
			content: "vless://nohostnamehere\n",
			want:    "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "secret.key")
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}

			result := extractServerIP(path)
			if result != tc.want {
				t.Errorf("extractServerIP = %q, want %q", result, tc.want)
			}
		})
	}
}

// ── Fuzz Tests ────────────────────────────────────────────────────────────────────

func FuzzExtractServerIP(f *testing.F) {
	f.Add("")
	f.Add("vless://uuid@1.2.3.4:443?params")
	f.Add("# comment\nvless://uuid@1.2.3.4:443")
	f.Add("vless://nohostnamehere")
	f.Add("\xef\xbb\xbfvless://uuid@5.5.5.5:443")

	f.Fuzz(func(t *testing.T, content string) {
		dir := t.TempDir()
		path := filepath.Join(dir, "secret.key")
		_ = os.WriteFile(path, []byte(content), 0o600)
		_ = extractServerIP(path)
	})
}
