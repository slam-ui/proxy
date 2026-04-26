package core

import (
	"fmt"
	"sync"
)

type State string

const (
	StateIdle       State = "idle"
	StateConnecting State = "connecting"
	StateRunning    State = "running"
	StateApplying   State = "applying"
	StateRestarting State = "restarting"
	StateError      State = "error"
)

type StateManager struct {
	mu    sync.RWMutex
	state State
}

func NewStateManager() *StateManager {
	return &StateManager{
		state: StateIdle,
	}
}

func (s *StateManager) Get() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

func (s *StateManager) Is(st State) bool {
	return s.Get() == st
}

func (s *StateManager) Transition(to State) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	from := s.state

	if !validTransition(from, to) {
		return fmt.Errorf("invalid transition: %s → %s", from, to)
	}

	s.state = to

	return nil
}

func (s *StateManager) MustTransition(to State) {
	if err := s.Transition(to); err != nil {
		panic(err)
	}
}

func validTransition(from, to State) bool {
	switch from {
	case StateIdle:
		return to == StateConnecting
	case StateConnecting:
		return to == StateRunning || to == StateError
	case StateRunning:
		return to == StateApplying || to == StateRestarting || to == StateError
	case StateApplying:
		return to == StateRestarting || to == StateRunning
	case StateRestarting:
		return to == StateRunning || to == StateError
	case StateError:
		return to == StateConnecting || to == StateIdle
	default:
		return false
	}
}
