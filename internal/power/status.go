package power

import "time"

const MinBatteryProbeInterval = 5 * time.Minute

type Status struct {
	Known          bool
	OnBattery      bool
	BatteryPercent int
}

func BatteryAwareInterval(base time.Duration, status Status) time.Duration {
	if base <= 0 {
		return base
	}
	if !status.Known || !status.OnBattery {
		return base
	}
	if base < MinBatteryProbeInterval {
		return MinBatteryProbeInterval
	}
	return base
}

func PauseBackgroundUpdates(status Status) bool {
	return status.Known && status.OnBattery
}
