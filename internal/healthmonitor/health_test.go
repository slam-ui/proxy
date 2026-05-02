package healthmonitor

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestScoreWeightsLatencyLossAndRecentSuccess(t *testing.T) {
	now := time.Unix(1000, 0)
	good := ServerHealth{
		LatencyMs:        []time.Duration{40 * time.Millisecond, 60 * time.Millisecond},
		PacketLoss:       0,
		LastSuccessfulAt: now.Add(-time.Minute),
	}
	bad := ServerHealth{
		LatencyMs:        []time.Duration{450 * time.Millisecond},
		PacketLoss:       0.5,
		LastSuccessfulAt: now.Add(-10 * time.Minute),
	}
	if Score(good, DefaultWeights, now) <= Score(bad, DefaultWeights, now) {
		t.Fatalf("good score must be higher: good=%f bad=%f", Score(good, DefaultWeights, now), Score(bad, DefaultWeights, now))
	}
	bad.ConsecutiveFails = 4
	if got := Score(bad, DefaultWeights, now); got != 0 {
		t.Fatalf("Score with >3 consecutive fails=%f, want 0", got)
	}
}

func TestMonitorRecordKeepsLastTenAndLoss(t *testing.T) {
	now := time.Unix(1000, 0)
	m := New(Options{Now: func() time.Time { return now }})
	for i := 0; i < 12; i++ {
		m.Record("a", time.Duration(i)*time.Millisecond, true)
	}
	m.Record("a", 0, false)
	got, ok := m.Get("a")
	if !ok {
		t.Fatal("missing health")
	}
	if len(got.LatencyMs) != 10 {
		t.Fatalf("latency samples=%d, want 10", len(got.LatencyMs))
	}
	if got.ConsecutiveFails != 1 {
		t.Fatalf("ConsecutiveFails=%d, want 1", got.ConsecutiveFails)
	}
	if got.PacketLoss <= 0 || got.Uptime >= 1 {
		t.Fatalf("unexpected loss/uptime: %+v", got)
	}
}

func TestMonitorStartStopsAndProbesTargets(t *testing.T) {
	var probes atomic.Int32
	m := New(Options{
		Interval: 10 * time.Millisecond,
		Targets: func() []ServerTarget {
			return []ServerTarget{{ID: "a", URL: "vless://id@example.com:443"}}
		},
		Probe: func(ctx context.Context, target ServerTarget) (time.Duration, error) {
			probes.Add(1)
			return time.Millisecond, nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.Start(ctx)
	}()
	time.Sleep(25 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("monitor did not stop")
	}
	if probes.Load() == 0 {
		t.Fatal("monitor did not probe")
	}
}

func TestProbeOnceRecordsFailures(t *testing.T) {
	m := New(Options{
		Targets: func() []ServerTarget {
			return []ServerTarget{{ID: "a", URL: "vless://id@example.com:443"}}
		},
		Probe: func(ctx context.Context, target ServerTarget) (time.Duration, error) {
			return 0, errors.New("timeout")
		},
	})
	m.ProbeOnce(context.Background())
	got, ok := m.Get("a")
	if !ok || got.ConsecutiveFails != 1 || got.Status != "unreachable" {
		t.Fatalf("unexpected failed health: %+v ok=%v", got, ok)
	}
}

func TestShouldFailoverHonorsCooldownAndImprovement(t *testing.T) {
	now := time.Unix(1000, 0)
	current := Snapshot{ID: "a", AverageLatencyMs: 700, Score: 0.3}
	best := Snapshot{ID: "b", AverageLatencyMs: 50, Score: 0.8}
	settings := DefaultDecisionSettings()
	ok, reason := ShouldFailover(current, best, time.Time{}, settings, now)
	if !ok || reason != "high latency" {
		t.Fatalf("ShouldFailover=%v %q, want high latency", ok, reason)
	}
	ok, reason = ShouldFailover(current, best, now.Add(-time.Minute), settings, now)
	if ok || reason != "cooldown" {
		t.Fatalf("cooldown ShouldFailover=%v %q", ok, reason)
	}
	best.Score = 0.35
	ok, reason = ShouldFailover(current, best, time.Time{}, settings, now)
	if ok || reason != "insufficient improvement" {
		t.Fatalf("small improvement ShouldFailover=%v %q", ok, reason)
	}
}
