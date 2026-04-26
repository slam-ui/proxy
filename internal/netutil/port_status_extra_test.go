package netutil

import (
	"strings"
	"testing"
	"time"
)

func TestGetPortStats_UsesFreshCache(t *testing.T) {
	want := &PortStats{
		MinPort:          1000,
		MaxPort:          1009,
		TotalPorts:       10,
		UnavailablePorts: 2,
		AvailablePorts:   8,
		AvailablePct:     80,
		IsCritical:       false,
	}

	cache.mu.Lock()
	cache.stats = want
	cache.when = time.Now()
	cache.ttl = time.Hour
	cache.mu.Unlock()
	defer ResetCache()

	got, err := GetPortStats()
	if err != nil {
		t.Fatalf("GetPortStats() error = %v", err)
	}
	if got != want {
		t.Fatalf("GetPortStats() did not return cached pointer")
	}
}

func TestResetCacheClearsCachedStats(t *testing.T) {
	cache.mu.Lock()
	cache.stats = &PortStats{AvailablePorts: 1}
	cache.when = time.Now()
	cache.mu.Unlock()

	ResetCache()

	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.stats != nil {
		t.Fatalf("cache.stats = %#v, want nil", cache.stats)
	}
	if !cache.when.IsZero() {
		t.Fatalf("cache.when = %v, want zero", cache.when)
	}
}

func TestFormatPortStatsIncludesKeyValues(t *testing.T) {
	got := FormatPortStats(&PortStats{
		TotalPorts:       100,
		UnavailablePorts: 25,
		AvailablePorts:   75,
		AvailablePct:     75,
	})
	for _, want := range []string{"75/100", "75.0%", "Available: 75", "Unavailable: 25"} {
		if !strings.Contains(got, want) {
			t.Fatalf("FormatPortStats() = %q, missing %q", got, want)
		}
	}
}
