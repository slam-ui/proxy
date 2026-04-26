//go:build windows
// +build windows

package api

import (
	"context"
	"testing"
	"time"

	"proxyclient/internal/logger"
	"proxyclient/internal/proxy"
)

// ── Server lifecycle ──────────────────────────────────────────────────────────

func TestNewServer_CreatesRouter(t *testing.T) {
	s := NewServer(Config{Logger: logger.New(logger.LevelInfo), ListenAddress: "127.0.0.1:0"}, context.Background())
	if s == nil {
		t.Fatal("NewServer returned nil")
	}
	if s.router == nil {
		t.Error("Router is nil")
	}
}

func TestNewServer_UsesBackgroundContext_WhenNil(t *testing.T) {
	s := NewServer(Config{Logger: logger.New(logger.LevelInfo)}, nil)
	if s.lifecycleCtx == nil {
		t.Error("lifecycleCtx should not be nil")
	}
}

func TestSetXRayManager_UpdatesManager(t *testing.T) {
	s := &Server{logger: logger.New(logger.LevelInfo), config: Config{Logger: logger.New(logger.LevelInfo)}}
	if s.GetXRayManager() != nil {
		t.Error("XRayManager should initially be nil")
	}
	mgr := &mockXRayManager{running: true}
	s.SetXRayManager(mgr)
	if s.GetXRayManager() != mgr {
		t.Error("XRayManager not updated")
	}
}

func TestIsWarming_ReturnsTrue_WhenNoManager(t *testing.T) {
	s := &Server{logger: logger.New(logger.LevelInfo), config: Config{Logger: logger.New(logger.LevelInfo)}}
	if !s.IsWarming() {
		t.Error("IsWarming should return true when no XRayManager")
	}
}

// ── Restart state ─────────────────────────────────────────────────────────────

func TestSetRestarting_SetsState(t *testing.T) {
	s := &Server{logger: logger.New(logger.LevelInfo)}
	readyAt := time.Now().Add(10 * time.Second)
	s.SetRestarting(readyAt)
	if !s.restarting {
		t.Error("restarting should be true")
	}
	if !s.restartReadyAt.Equal(readyAt) {
		t.Error("restartReadyAt not set correctly")
	}
}

func TestClearRestarting_ResetsState(t *testing.T) {
	s := &Server{
		logger:        logger.New(logger.LevelInfo),
		restarting:    true,
		tunAttempt:    3,
		tunMaxAttempt: 5,
	}
	s.ClearRestarting()
	if s.restarting {
		t.Error("restarting should be false")
	}
	if s.tunAttempt != 0 {
		t.Error("tunAttempt should be 0")
	}
}

func TestSetTunAttempt_UpdatesCounters(t *testing.T) {
	s := &Server{logger: logger.New(logger.LevelInfo)}
	s.SetTunAttempt(2, 5)
	if s.tunAttempt != 2 {
		t.Errorf("tunAttempt = %d, want 2", s.tunAttempt)
	}
	if s.tunMaxAttempt != 5 {
		t.Errorf("tunMaxAttempt = %d, want 5", s.tunMaxAttempt)
	}
}

// ── Mock types (shared across server_*_test.go files) ────────────────────────

type mockProxyManager struct {
	enabled bool
	address string
	err     error
}

func (m *mockProxyManager) Enable(cfg proxy.Config) error {
	if m.err != nil {
		return m.err
	}
	m.enabled = true
	m.address = cfg.Address
	return nil
}

func (m *mockProxyManager) Disable() error {
	if m.err != nil {
		return m.err
	}
	m.enabled = false
	return nil
}

func (m *mockProxyManager) IsEnabled() bool                                              { return m.enabled }
func (m *mockProxyManager) GetConfig() proxy.Config                                      { return proxy.Config{Address: m.address} }
func (m *mockProxyManager) StartGuard(ctx context.Context, interval time.Duration) error { return nil }
func (m *mockProxyManager) StopGuard()                                                   {}
func (m *mockProxyManager) PauseGuard(d time.Duration)                                   {} // БАГ 14
func (m *mockProxyManager) ResumeGuard()                                                 {} // БАГ 14

type mockXRayManager struct {
	running bool
	pid     int
	err     error
}

func (m *mockXRayManager) Start() error                          { return m.err }
func (m *mockXRayManager) StartAfterManualCleanup() error        { return m.err }
func (m *mockXRayManager) Stop() error                           { return m.err }
func (m *mockXRayManager) IsRunning() bool                       { return m.running }
func (m *mockXRayManager) GetPID() int                           { return m.pid }
func (m *mockXRayManager) Wait() error                           { return m.err }
func (m *mockXRayManager) LastOutput() string                    { return "" }
func (m *mockXRayManager) Uptime() time.Duration                 { return 0 }
func (m *mockXRayManager) GetHealthStatus() (int, float64, bool) { return 0, 0, false }
