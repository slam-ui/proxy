//go:build !windows

package wintun

import "time"

// EstimateReadyAt is a non-Windows stub used by API tests and cross-platform builds.
func EstimateReadyAt() time.Time {
	return time.Now()
}
