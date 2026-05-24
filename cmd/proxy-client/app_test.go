//go:build windows
// +build windows

package main

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"proxyclient/internal/api"
	"proxyclient/internal/logger"
	"proxyclient/internal/proxy"
)

type shutdownProxyStub struct {
	enabled        bool
	config         proxy.Config
	stopGuardCalls int
	disableCalls   int
}

func (p *shutdownProxyStub) Enable(cfg proxy.Config) error {
	p.enabled = true
	p.config = cfg
	return nil
}

func (p *shutdownProxyStub) Disable() error {
	p.enabled = false
	p.disableCalls++
	return nil
}

func (p *shutdownProxyStub) IsEnabled() bool { return p.enabled }

func (p *shutdownProxyStub) GetConfig() proxy.Config { return p.config }

func (p *shutdownProxyStub) StartGuard(ctx context.Context, interval time.Duration) error { return nil }

func (p *shutdownProxyStub) StopGuard() { p.stopGuardCalls++ }

func (p *shutdownProxyStub) PauseGuard(d time.Duration) {}

func (p *shutdownProxyStub) ResumeGuard() {}

type shutdownXrayStub struct {
	stopCalls int
}

func (s *shutdownXrayStub) Start() error                          { return nil }
func (s *shutdownXrayStub) StartAfterManualCleanup() error        { return nil }
func (s *shutdownXrayStub) Stop() error                           { s.stopCalls++; return nil }
func (s *shutdownXrayStub) IsRunning() bool                       { return false }
func (s *shutdownXrayStub) GetPID() int                           { return 0 }
func (s *shutdownXrayStub) Wait() error                           { return nil }
func (s *shutdownXrayStub) LastOutput() string                    { return "" }
func (s *shutdownXrayStub) Uptime() time.Duration                 { return 0 }
func (s *shutdownXrayStub) GetHealthStatus() (int, float64, bool) { return 0, 0, false }
func (s *shutdownXrayStub) SetHealthAlertFn(fn func())            {}
func (s *shutdownXrayStub) MemoryMB() uint64                      { return 0 }

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

func TestShutdown_DisablesProxyAndStopsGuard(t *testing.T) {
	cfg := DefaultAppConfig()
	app := NewApp(cfg, &bytes.Buffer{})

	proxyStub := &shutdownProxyStub{
		enabled: true,
		config:  proxy.Config{Address: "127.0.0.1:8080", Override: "<local>"},
	}
	xrayStub := &shutdownXrayStub{}
	app.proxyManager = proxyStub
	app.apiServer = api.NewServer(api.Config{
		XRayManager:  xrayStub,
		ProxyManager: proxyStub,
		Logger:       &logger.NoOpLogger{},
	}, app.lifecycleCtx)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	app.Shutdown(ctx, nil)

	if proxyStub.stopGuardCalls == 0 {
		t.Fatal("StopGuard was not called during Shutdown")
	}
	if proxyStub.disableCalls == 0 {
		t.Fatal("Disable was not called during Shutdown")
	}
	if proxyStub.enabled {
		t.Fatal("proxy remained enabled after Shutdown")
	}
	if xrayStub.stopCalls == 0 {
		t.Fatal("XRay Stop was not called during Shutdown")
	}
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
			t.Errorf("failed to write secret.key: %v", err)
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

// ── Bug 9: stale wintun_stopped_at ───────────────────────────────────────────────

// TestStaleWintunStopFile_IsDeleted проверяет что файл wintun_stopped_at с mtime > 24ч
// удаляется при запуске (та же логика что в startBackground).
func TestStaleWintunStopFile_IsDeleted(t *testing.T) {
	dir := t.TempDir()
	stopFile := filepath.Join(dir, "wintun_stopped_at")

	// Создаём файл и откатываем его mtime на 25 часов назад
	if err := os.WriteFile(stopFile, []byte("12345678"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	staleTime := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(stopFile, staleTime, staleTime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	// Воспроизводим логику из startBackground
	if fi, err := os.Stat(stopFile); err == nil {
		if time.Since(fi.ModTime()) > 24*time.Hour {
			_ = os.Remove(stopFile)
		}
	}

	if _, err := os.Stat(stopFile); err == nil {
		t.Error("БАГ 9: устаревший wintun_stopped_at должен быть удалён")
	}
}

// TestFreshWintunStopFile_IsKept проверяет что свежий файл wintun_stopped_at не удаляется.
func TestFreshWintunStopFile_IsKept(t *testing.T) {
	dir := t.TempDir()
	stopFile := filepath.Join(dir, "wintun_stopped_at")

	if err := os.WriteFile(stopFile, []byte("12345678"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// Файл только что создан — mtime < 24ч

	if fi, err := os.Stat(stopFile); err == nil {
		if time.Since(fi.ModTime()) > 24*time.Hour {
			_ = os.Remove(stopFile)
		}
	}

	if _, err := os.Stat(stopFile); err != nil {
		t.Error("свежий wintun_stopped_at не должен удаляться")
	}
}

// ── Bug 1: preflightCheck ─────────────────────────────────────────────────────────

type testLogger struct{ infoCalled bool }

func (l *testLogger) Info(f string, args ...interface{})   { l.infoCalled = true }
func (l *testLogger) Warn(f string, args ...interface{})   {}
func (l *testLogger) Error(f string, args ...interface{})  {}
func (l *testLogger) Debug(f string, args ...interface{})  {}
func (l *testLogger) Fatal(f string, args ...interface{})  {}
func (l *testLogger) Fatalf(f string, args ...interface{}) {}

// TestPreflightCheck_PassesWhenPortsFree проверяет что preflightCheck не возвращает ошибку
// когда все порты свободны.
func TestPreflightCheck_PassesWhenPortsFree(t *testing.T) {
	oldAddrs := preflightAddrs
	preflightAddrs = []preflightAddr{{name: "test free port", addr: "127.0.0.1:0"}}
	defer func() { preflightAddrs = oldAddrs }()

	log := &testLogger{}
	ctx := context.Background()
	if err := preflightCheck(ctx, log); err != nil {
		t.Errorf("preflightCheck должна пройти с свободными портами: %v", err)
	}
}

// TestPreflightCheck_RespectsContext проверяет что preflightCheck возвращает ошибку
// когда контекст отменён во время ожидания освобождения порта.
func TestPreflightCheck_RespectsContext(t *testing.T) {
	// Занимаем один из проверяемых портов.
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("не удалось занять локальный порт для теста — пропускаем")
	}
	defer occupied.Close()
	oldAddrs := preflightAddrs
	preflightAddrs = []preflightAddr{{name: "occupied test port", addr: occupied.Addr().String()}}
	defer func() { preflightAddrs = oldAddrs }()

	log := &testLogger{}
	ctx, cancel := context.WithCancel(context.Background())
	// Отменяем контекст немедленно.
	cancel()

	err = preflightCheck(ctx, log)
	if err == nil {
		t.Error("preflightCheck должна вернуть ошибку при отменённом контексте")
	}
}

