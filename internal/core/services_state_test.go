package core

import (
	"context"
	"errors"
	"testing"
	"time"
)

type testXRayService struct{}

func (testXRayService) Start() error                          { return nil }
func (testXRayService) StartAfterManualCleanup() error        { return nil }
func (testXRayService) Stop() error                           { return nil }
func (testXRayService) IsRunning() bool                       { return true }
func (testXRayService) GetPID() int                           { return 42 }
func (testXRayService) Wait() error                           { return nil }
func (testXRayService) LastOutput() string                    { return "" }
func (testXRayService) Uptime() time.Duration                 { return time.Second }
func (testXRayService) GetHealthStatus() (int, float64, bool) { return 0, 0, false }

type testProxyService struct{}

func (testProxyService) Enable(_, _ string) error                        { return nil }
func (testProxyService) Disable() error                                  { return nil }
func (testProxyService) IsEnabled() bool                                 { return true }
func (testProxyService) GetAddress() string                              { return "127.0.0.1:10807" }
func (testProxyService) StartGuard(context.Context, time.Duration) error { return nil }
func (testProxyService) StopGuard()                                      {}

type testLogService struct{}

func (testLogService) Info(string, ...interface{})  {}
func (testLogService) Error(string, ...interface{}) {}
func (testLogService) Debug(string, ...interface{}) {}
func (testLogService) Warn(string, ...interface{})  {}

func TestServicesValidate_RequiresOnlyMandatoryServices(t *testing.T) {
	s := &Services{
		XRay:  testXRayService{},
		Proxy: testProxyService{},
		Log:   testLogService{},
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestServicesValidate_MissingServices(t *testing.T) {
	tests := []struct {
		name string
		s    Services
		want string
	}{
		{name: "xray", s: Services{}, want: "XRay"},
		{name: "proxy", s: Services{XRay: testXRayService{}}, want: "Proxy"},
		{name: "log", s: Services{XRay: testXRayService{}, Proxy: testProxyService{}}, want: "Log"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.s.Validate()
			if err == nil {
				t.Fatal("Validate() = nil, want missing service error")
			}
			var missing *MissingServiceError
			if !errors.As(err, &missing) {
				t.Fatalf("Validate() error type = %T, want *MissingServiceError", err)
			}
			if missing.Name != tt.want {
				t.Fatalf("MissingServiceError.Name = %q, want %q", missing.Name, tt.want)
			}
		})
	}
}

func TestStateManager_ValidLifecycle(t *testing.T) {
	sm := NewStateManager()
	if !sm.Is(StateIdle) {
		t.Fatalf("initial state = %q, want idle", sm.Get())
	}

	for _, st := range []State{StateConnecting, StateRunning, StateApplying, StateRestarting, StateRunning, StateError, StateIdle} {
		if err := sm.Transition(st); err != nil {
			t.Fatalf("Transition(%s) failed: %v", st, err)
		}
		if !sm.Is(st) {
			t.Fatalf("state after transition = %q, want %q", sm.Get(), st)
		}
	}
}

func TestStateManager_InvalidTransitionDoesNotChangeState(t *testing.T) {
	sm := NewStateManager()
	if err := sm.Transition(StateRunning); err == nil {
		t.Fatal("idle -> running should be invalid")
	}
	if got := sm.Get(); got != StateIdle {
		t.Fatalf("invalid transition changed state to %q", got)
	}
}

func TestStateManager_MustTransitionPanicsOnInvalidTransition(t *testing.T) {
	sm := NewStateManager()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("MustTransition should panic on invalid transition")
		}
	}()
	sm.MustTransition(StateRunning)
}
