package healthmonitor

import (
	"context"
	"math"
	"sort"
	"sync"
	"time"
)

const maxLatencySamples = 10

type ServerTarget struct {
	ID  string
	URL string
}

type ServerHealth struct {
	LatencyMs        []time.Duration `json:"latency_ms"`
	PacketLoss       float64         `json:"packet_loss"`
	LastSuccessfulAt time.Time       `json:"last_successful_at,omitempty"`
	LastErrorAt      time.Time       `json:"last_error_at,omitempty"`
	ConsecutiveFails int             `json:"consecutive_fails"`
	Uptime           float64         `json:"uptime"`
}

type Snapshot struct {
	ServerHealth
	ID               string  `json:"id"`
	AverageLatencyMs int64   `json:"average_latency_ms"`
	Score            float64 `json:"score"`
	Recommended      bool    `json:"recommended"`
	Status           string  `json:"status"`
}

type Weights struct {
	Latency float64 `json:"latency"`
	Loss    float64 `json:"loss"`
	Success float64 `json:"success"`
}

var DefaultWeights = Weights{Latency: 0.5, Loss: 0.3, Success: 0.2}

type ProbeFunc func(ctx context.Context, target ServerTarget) (time.Duration, error)

type Monitor struct {
	mu        sync.RWMutex
	health    map[string]*state
	now       func() time.Time
	probe     ProbeFunc
	targetsFn func() []ServerTarget
	interval  time.Duration
	weights   Weights
}

type state struct {
	ServerHealth
	checks   int
	success  int
	failures int
}

type Options struct {
	Probe    ProbeFunc
	Targets  func() []ServerTarget
	Interval time.Duration
	Weights  Weights
	Now      func() time.Time
}

func New(opts Options) *Monitor {
	if opts.Interval <= 0 {
		opts.Interval = 30 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Weights == (Weights{}) {
		opts.Weights = DefaultWeights
	}
	return &Monitor{
		health:    map[string]*state{},
		now:       opts.Now,
		probe:     opts.Probe,
		targetsFn: opts.Targets,
		interval:  opts.Interval,
		weights:   opts.Weights,
	}
}

func (m *Monitor) Start(ctx context.Context) {
	m.ProbeOnce(ctx)
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.ProbeOnce(ctx)
		}
	}
}

func (m *Monitor) ProbeOnce(ctx context.Context) {
	if m.probe == nil || m.targetsFn == nil {
		return
	}
	targets := m.targetsFn()
	var wg sync.WaitGroup
	for _, target := range targets {
		if target.ID == "" || target.URL == "" {
			continue
		}
		wg.Add(1)
		go func(t ServerTarget) {
			defer wg.Done()
			latency, err := m.probe(ctx, t)
			m.Record(t.ID, latency, err == nil)
		}(target)
	}
	wg.Wait()
}

func (m *Monitor) Record(id string, latency time.Duration, ok bool) {
	if id == "" {
		return
	}
	now := m.now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.health[id]
	if h == nil {
		h = &state{}
		m.health[id] = h
	}
	h.checks++
	if ok {
		h.success++
		h.ConsecutiveFails = 0
		h.LastSuccessfulAt = now
		if latency > 0 {
			h.LatencyMs = append(h.LatencyMs, latency)
			if len(h.LatencyMs) > maxLatencySamples {
				h.LatencyMs = h.LatencyMs[len(h.LatencyMs)-maxLatencySamples:]
			}
		}
	} else {
		h.failures++
		h.ConsecutiveFails++
		h.LastErrorAt = now
	}
	h.PacketLoss = float64(h.failures) / float64(h.checks)
	h.Uptime = float64(h.success) / float64(h.checks)
}

func (m *Monitor) MarkFailure(id string) {
	m.Record(id, 0, false)
}

func (m *Monitor) Snapshot() []Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Snapshot, 0, len(m.health))
	bestID := ""
	bestScore := -1.0
	now := m.now().UTC()
	for id, h := range m.health {
		score := Score(h.ServerHealth, m.weights, now)
		if score > bestScore {
			bestID = id
			bestScore = score
		}
		out = append(out, snapshotFromHealth(id, h.ServerHealth, score))
	}
	for i := range out {
		out[i].Recommended = out[i].ID == bestID && out[i].Score > 0
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (m *Monitor) Get(id string) (Snapshot, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	h := m.health[id]
	if h == nil {
		return Snapshot{ID: id, Status: "unknown"}, false
	}
	return snapshotFromHealth(id, h.ServerHealth, Score(h.ServerHealth, m.weights, m.now().UTC())), true
}

func Score(h ServerHealth, weights Weights, now time.Time) float64 {
	if weights == (Weights{}) {
		weights = DefaultWeights
	}
	if h.ConsecutiveFails > 3 {
		return 0
	}
	avg := AverageLatency(h.LatencyMs)
	latencyScore := 0.0
	if avg > 0 {
		latencyScore = math.Max(0, 1.0-float64(avg)/float64(500*time.Millisecond))
	}
	lossScore := math.Max(0, 1.0-h.PacketLoss)
	successScore := 1.0
	if h.LastSuccessfulAt.IsZero() || now.Sub(h.LastSuccessfulAt) > 5*time.Minute {
		successScore = 0.5
	}
	total := weights.Latency + weights.Loss + weights.Success
	if total <= 0 {
		return 0
	}
	score := (latencyScore*weights.Latency + lossScore*weights.Loss + successScore*weights.Success) / total
	if score < 0 {
		return 0
	}
	if score > 1 {
		return 1
	}
	return score
}

func Best(snapshots []Snapshot) (Snapshot, bool) {
	var best Snapshot
	found := false
	for _, s := range snapshots {
		if s.Score <= 0 || s.ConsecutiveFails > 3 {
			continue
		}
		if !found || s.Score > best.Score {
			best = s
			found = true
		}
	}
	return best, found
}

type DecisionSettings struct {
	MaxLatency         time.Duration
	MaxConsecutiveFail int
	MinScoreGain       float64
	FlapCooldown       time.Duration
}

func DefaultDecisionSettings() DecisionSettings {
	return DecisionSettings{
		MaxLatency:         500 * time.Millisecond,
		MaxConsecutiveFail: 3,
		MinScoreGain:       0.10,
		FlapCooldown:       5 * time.Minute,
	}
}

func ShouldFailover(current Snapshot, best Snapshot, lastSwitch time.Time, settings DecisionSettings, now time.Time) (bool, string) {
	if settings == (DecisionSettings{}) {
		settings = DefaultDecisionSettings()
	}
	if best.ID == "" || best.ID == current.ID || best.Score <= 0 {
		return false, ""
	}
	if !lastSwitch.IsZero() && now.Sub(lastSwitch) < settings.FlapCooldown {
		return false, "cooldown"
	}
	degraded := false
	reason := ""
	if current.ConsecutiveFails >= settings.MaxConsecutiveFail {
		degraded = true
		reason = "consecutive failures"
	}
	if current.AverageLatencyMs > 0 && time.Duration(current.AverageLatencyMs)*time.Millisecond > settings.MaxLatency {
		degraded = true
		reason = "high latency"
	}
	if !degraded {
		return false, ""
	}
	if best.Score-current.Score < settings.MinScoreGain {
		return false, "insufficient improvement"
	}
	return true, reason
}

func AverageLatency(values []time.Duration) time.Duration {
	if len(values) == 0 {
		return 0
	}
	var total time.Duration
	for _, v := range values {
		total += v
	}
	return total / time.Duration(len(values))
}

func snapshotFromHealth(id string, h ServerHealth, score float64) Snapshot {
	avg := AverageLatency(h.LatencyMs)
	return Snapshot{
		ID:               id,
		ServerHealth:     h,
		AverageLatencyMs: avg.Milliseconds(),
		Score:            score,
		Status:           statusFor(h, avg),
	}
}

func statusFor(h ServerHealth, avg time.Duration) string {
	if h.LastSuccessfulAt.IsZero() && h.LastErrorAt.IsZero() {
		return "unknown"
	}
	if h.ConsecutiveFails > 0 && h.LastErrorAt.After(h.LastSuccessfulAt) {
		return "unreachable"
	}
	if avg > 300*time.Millisecond || h.PacketLoss > 0.20 {
		return "bad"
	}
	if avg > 100*time.Millisecond || h.PacketLoss > 0.05 {
		return "warn"
	}
	return "ok"
}
