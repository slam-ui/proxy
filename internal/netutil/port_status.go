package netutil

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// PortStats contains statistics about available ephemeral ports on the system.
// БАГ #2B: используется для детектирования исчерпания портов.
type PortStats struct {
	// Ephemeral port range (typically 49152-65535 on Windows)
	MinPort int
	MaxPort int
	// Total available ports in the range
	TotalPorts int
	// Ports in TIME_WAIT or other non-available state
	UnavailablePorts int
	// Estimated available ports
	AvailablePorts int
	// Percentage of available ports (0-100)
	AvailablePct float64
	// Whether we're below critical threshold
	IsCritical bool
}

// portStatsCache holds the last computed PortStats with a TTL
type portStatsCache struct {
	mu    sync.Mutex
	stats *PortStats
	when  time.Time
	ttl   time.Duration
}

var cache = &portStatsCache{
	ttl: 5 * time.Second, // refresh every 5 seconds
}

// GetPortStats returns current port availability statistics.
// Uses netstat to count TIME_WAIT and other non-available ports.
// БАГ #2B: if >95% ports are unavailable or available <1000, marks as critical.
func GetPortStats() (*PortStats, error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	// Check cache
	if cache.stats != nil && time.Since(cache.when) < cache.ttl {
		return cache.stats, nil
	}

	// Standard Windows ephemeral port range (can vary)
	minPort := 49152
	maxPort := 65535
	totalPorts := maxPort - minPort + 1

	// Run netstat to count TIME_WAIT ports
	timeWaitCount, err := countPortState("TIME_WAIT")
	if err != nil {
		// Fallback: can't determine, assume no crisis yet
		return &PortStats{
			MinPort:      minPort,
			MaxPort:      maxPort,
			TotalPorts:   totalPorts,
			AvailablePct: 100,
			IsCritical:   false,
		}, nil
	}

	// Also count CLOSE_WAIT and other problematic states
	closeWaitCount, _ := countPortState("CLOSE_WAIT")

	unavailable := timeWaitCount + closeWaitCount
	available := totalPorts - unavailable
	availablePct := (float64(available) / float64(totalPorts)) * 100

	// Critical if <5% available or <1000 ports free
	isCritical := available < 1000 || availablePct < 5

	stats := &PortStats{
		MinPort:          minPort,
		MaxPort:          maxPort,
		TotalPorts:       totalPorts,
		UnavailablePorts: unavailable,
		AvailablePorts:   available,
		AvailablePct:     availablePct,
		IsCritical:       isCritical,
	}

	cache.stats = stats
	cache.when = time.Now()
	return stats, nil
}

// countPortState runs netstat and counts ports in a specific state
func countPortState(state string) (int, error) {
	// Run: netstat -ano | findstr /C:"STATE"
	// Output format: "  TCP    0.0.0.0:49152         0.0.0.0:0              TIME_WAIT       12345"
	cmd := exec.Command("netstat", "-ano")
	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("netstat failed: %w", err)
	}

	lines := strings.Split(string(output), "\n")
	stateRe := regexp.MustCompile(`\s+` + state + `\s+`)
	count := 0

	for _, line := range lines {
		if stateRe.MatchString(line) {
			count++
		}
	}

	return count, nil
}

// IsPortRangeExhausted returns true if ephemeral ports are critically depleted.
// БАГ #2B: used by health monitoring to alert user about port exhaustion.
func IsPortRangeExhausted() bool {
	stats, err := GetPortStats()
	if err != nil {
		return false // can't determine
	}
	return stats.IsCritical
}

// FormatPortStats returns a human-readable description of port statistics.
func FormatPortStats(stats *PortStats) string {
	return fmt.Sprintf(
		"Ports: %d/%d available (%.1f%%) | Available: %d | Unavailable: %d",
		stats.AvailablePorts,
		stats.TotalPorts,
		stats.AvailablePct,
		stats.AvailablePorts,
		stats.UnavailablePorts,
	)
}

// ResetCache clears the port stats cache (useful after restart or troubleshooting)
func ResetCache() {
	cache.mu.Lock()
	cache.stats = nil
	cache.when = time.Time{}
	cache.mu.Unlock()
}
