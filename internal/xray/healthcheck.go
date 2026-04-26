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
// HealthChecker детектирует этот случай: если >= thresholdCount ошибок за windowDuration
// → отправить notification. Счётчик connectionCount удалён: TrackConnectionAttempt()
// никогда не вызывался из production-кода, поэтому rate-based алерт всегда давал 0%.
// BUG-NEW-6 FIX: переключились на count-based alerting — не нужен denominator.
type HealthChecker struct {
	mu             sync.Mutex
	errors         []ConnectionError
	windowDuration time.Duration // sliding window (default 30s)
	thresholdCount int           // число ошибок в окне для триггера алерта (default 5)
	lastAlertTime  time.Time
	alertCooldown  time.Duration // не алертим чаще чем раз в X секунд
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
// Default: 30-second window, 5-error count threshold, 60-second cooldown between alerts.
func NewHealthChecker() *HealthChecker {
	return &HealthChecker{
		errors:         make([]ConnectionError, 0, 1000),
		windowDuration: 30 * time.Second,
		thresholdCount: 5, // alert if >= 5 errors in the window
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
	// BUG FIX: когда ВСЕ ошибки старше cutoff, validIdx оставался 0 и условие
	// validIdx > 0 не выполнялось — старые ошибки не удалялись (утечка памяти
	// и ложные алерты). Используем found-флаг чтобы различать два случая:
	// found=false означает "все старые" → очищаем весь слайс.
	cutoff := time.Now().Add(-hc.windowDuration)
	found := false
	validIdx := 0
	for i, err := range hc.errors {
		if err.Timestamp.After(cutoff) {
			validIdx = i
			found = true
			break
		}
	}
	if !found {
		hc.errors = hc.errors[:0] // все ошибки устарели — очищаем
	} else if validIdx > 0 {
		hc.errors = hc.errors[validIdx:]
	}

	// BUG-NEW-6 FIX: count-based alerting — не нужен denominator connectionCount.
	// Проверяем порог и обновляем lastAlertTime внутри shouldAlert.

	// Check threshold
	return hc.shouldAlert()
}

// shouldAlert returns true if error count in window reaches threshold and cooldown has passed.
// Caller must hold hc.mu.
func (hc *HealthChecker) shouldAlert() bool {
	if len(hc.errors) < hc.thresholdCount {
		return false
	}

	now := time.Now()
	shouldTrigger := now.Sub(hc.lastAlertTime) > hc.alertCooldown
	if shouldTrigger {
		hc.lastAlertTime = now
	}
	return shouldTrigger
}

// GetStatus returns current error count and alert state.
// BUG-NEW-6 FIX: возвращает count-based метрику вместо rate (denominator connectionCount
// никогда не инкрементировался в production → rate всегда был 0%).
// errorRatePct теперь = (errorCount / thresholdCount) * 100 — показывает насыщенность
// буфера алертов (0% = нет ошибок, 100% = порог достигнут, >100% = превышен).
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

	pct := float64(validCount) / float64(hc.thresholdCount) * 100
	return validCount, pct, validCount >= hc.thresholdCount
}

// Reset clears all tracked errors.
func (hc *HealthChecker) Reset() {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	hc.errors = hc.errors[:0]
	hc.lastAlertTime = time.Time{}
}

// SetThresholds allows customization of detection parameters.
// errorThresholdCount: минимальное число ошибок в окне для триггера алерта (0 = не менять).
func (hc *HealthChecker) SetThresholds(windowDuration time.Duration, errorThresholdCount int, alertCooldown time.Duration) {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	if windowDuration > 0 {
		hc.windowDuration = windowDuration
	}
	if errorThresholdCount > 0 {
		hc.thresholdCount = errorThresholdCount
	}
	if alertCooldown > 0 {
		hc.alertCooldown = alertCooldown
	}
}
