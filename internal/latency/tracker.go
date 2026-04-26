package latency

import (
	"sync"
	"time"
)

const maxPoints = 120

type Point struct {
	TS int64 `json:"ts"`
	Ms int64 `json:"ms"`
}

type Tracker struct {
	mu      sync.RWMutex
	history map[string][]Point
}

var Global = &Tracker{history: make(map[string][]Point)}

func (t *Tracker) Record(serverID string, ms int64) {
	if serverID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	pts := append(t.history[serverID], Point{TS: time.Now().Unix(), Ms: ms})
	if len(pts) > maxPoints {
		pts = pts[len(pts)-maxPoints:]
	}
	t.history[serverID] = pts
}

func (t *Tracker) Get(serverID string) []Point {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return append([]Point(nil), t.history[serverID]...)
}
