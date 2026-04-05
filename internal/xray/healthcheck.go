package xray

import (
	"regexp"
	"sync"
	"time"
)

// ConnectionError represents a tracked connection error from sing-box logs.
type ConnectionError struct {
	Timestamp time.Time
	ErrorType string // "wsarecv", "timeout", "bind", etc.
	Outbound  string // outbound name (e.g., "vless", "direct")
	Message   string // full error message
}

// HealthChecker tracks connection errors over a sliding time window
// and determines if the VLESS service is degraded or unavailable.
//
// БАГ #3: sing-box может быть недоступен на 9+ минут.
// HealthChecker детектирует этот случай: если >X% соединений за последние N секунд
// упали с таймаутом или другими ошибками → отправить notification.
type HealthChecker struct {
	mu              sync.Mutex
	errors          []ConnectionError
	windowDuration  time.Duration // sliding window for error rate (default 30s)
	thresholdPct    float64       // % of failed connections to trigger alert (default 50%)
	lastAlertTime   time.Time
	alertCooldown   time.Duration // don't alert more than once per X seconds
	connectionCount int           // total attempts in current window (for rate calculation)
}

// ErrorPattern represents a pattern to extract connection errors from sing-box logs
var connectionErrorPatterns = []struct {
	pattern   *regexp.Regexp
	errorType string
}{
	{regexp.MustCompile(`wsarecv:.*A connection attempt failed because the connected party did not properly respond`), "timeout"},
	{regexp.MustCompile(`i/o timeout`), "timeout"},
	{regexp.MustCompile(`dial tcp.*: i/o timeout`), "dial_timeout"},
	{regexp.MustCompile(`bind:.*An operation on a socket could not be performed`), "bind_error"},
	{regexp.MustCompile(`connection: open connection.*using outbound/vless.*: (wsarecv|dial|timeout)`), "vless_error"},
	{regexp.MustCompile(`ERROR.*connection.*outbound/vless`), "vless_connection"},
	{regexp.MustCompile(`broken pipe|connection reset`), "connection_reset"},
}

// NewHealthChecker creates a new health checker with default configuration.
// Default: 30-second window, 50% error threshold, 60-second cooldown between alerts.
func NewHealthChecker() *HealthChecker {
	return &HealthChecker{
		errors:         make([]ConnectionError, 0, 1000),
		windowDuration: 30 * time.Second,
		thresholdPct:   50.0, // alert if >50% connections failed
		alertCooldown:  60 * time.Second,
	}
}

// RecordError registers a new connection error.
// Returns true if this error triggers a health alert threshold.
func (hc *HealthChecker) RecordError(timestamp time.Time, errorType, outbound, message string) bool {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	// Append error to window
	hc.errors = append(hc.errors, ConnectionError{
		Timestamp: timestamp,
		ErrorType: errorType,
		Outbound:  outbound,
		Message:   message,
	})

	// Prune old errors outside window
	cutoff := time.Now().Add(-hc.windowDuration)
	validIdx := 0
	for i, err := range hc.errors {
		if err.Timestamp.After(cutoff) {
			validIdx = i
			break
		}
	}
	if validIdx > 0 {
		hc.errors = hc.errors[validIdx:]
	}

	// Count total attempts (sample: for every error, assume ~3-5 total attempts)
	// This is a heuristic since we don't see every connection attempt, only errors.
	hc.connectionCount = len(hc.errors) * 4 // rough estimate

	// Check threshold
	return hc.shouldAlert()
}

// TrackConnectionAttempt increments the connection counter (called for each processed connection).
func (hc *HealthChecker) TrackConnectionAttempt() {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	hc.connectionCount++
}

// shouldAlert returns true if error rate exceeds threshold and cooldown has passed.
func (hc *HealthChecker) shouldAlert() bool {
	if hc.connectionCount == 0 {
		return false
	}

	errorRate := (float64(len(hc.errors)) / float64(hc.connectionCount)) * 100
	now := time.Now()

	// Check if alert should be triggered
	shouldTrigger := errorRate > hc.thresholdPct &&
		now.Sub(hc.lastAlertTime) > hc.alertCooldown

	if shouldTrigger {
		hc.lastAlertTime = now
	}

	return shouldTrigger
}

// GetStatus returns current error rate and alert state.
func (hc *HealthChecker) GetStatus() (errorCount int, errorRatePct float64, wouldAlert bool) {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	cutoff := time.Now().Add(-hc.windowDuration)
	validCount := 0
	for _, err := range hc.errors {
		if err.Timestamp.After(cutoff) {
			validCount++
		}
	}

	if hc.connectionCount == 0 {
		return validCount, 0, false
	}

	ratePct := (float64(validCount) / float64(hc.connectionCount)) * 100
	return validCount, ratePct, ratePct > hc.thresholdPct
}

// Reset clears all tracked errors.
func (hc *HealthChecker) Reset() {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	hc.errors = hc.errors[:0]
	hc.connectionCount = 0
	hc.lastAlertTime = time.Time{}
}

// SetThresholds allows customization of detection parameters.
func (hc *HealthChecker) SetThresholds(windowDuration time.Duration, errorThresholdPct float64, alertCooldown time.Duration) {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	if windowDuration > 0 {
		hc.windowDuration = windowDuration
	}
	if errorThresholdPct > 0 {
		hc.thresholdPct = errorThresholdPct
	}
	if alertCooldown > 0 {
		hc.alertCooldown = alertCooldown
	}
}
