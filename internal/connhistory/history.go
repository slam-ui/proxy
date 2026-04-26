package connhistory

import (
	"sync"
	"time"
)

type EventKind string

const (
	EventConnect    EventKind = "connect"
	EventDisconnect EventKind = "disconnect"
	EventFailover   EventKind = "failover"
	EventReconnect  EventKind = "reconnect"
	EventNetChange  EventKind = "net_change"
)

type Event struct {
	Time      time.Time `json:"time"`
	Kind      EventKind `json:"kind"`
	Server    string    `json:"server,omitempty"`
	LatencyMs int64     `json:"latency_ms,omitempty"`
	Reason    string    `json:"reason,omitempty"`
}

type History struct {
	mu     sync.RWMutex
	events []Event
}

var Global = &History{}

func (h *History) Add(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, e)
	if len(h.events) > 100 {
		h.events = h.events[len(h.events)-100:]
	}
}

func (h *History) All() []Event {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]Event, len(h.events))
	copy(out, h.events)
	return out
}
