package power

import (
	"testing"
	"time"
)

func TestBatteryAwareIntervalRaisesShortProbeOnBattery(t *testing.T) {
	got := BatteryAwareInterval(60*time.Second, Status{Known: true, OnBattery: true})
	if got != MinBatteryProbeInterval {
		t.Fatalf("BatteryAwareInterval = %s, want %s", got, MinBatteryProbeInterval)
	}
}

func TestBatteryAwareIntervalKeepsACInterval(t *testing.T) {
	got := BatteryAwareInterval(60*time.Second, Status{Known: true, OnBattery: false})
	if got != 60*time.Second {
		t.Fatalf("BatteryAwareInterval = %s, want 60s", got)
	}
}

func TestPauseBackgroundUpdatesOnlyOnKnownBattery(t *testing.T) {
	if !PauseBackgroundUpdates(Status{Known: true, OnBattery: true}) {
		t.Fatal("PauseBackgroundUpdates returned false on battery")
	}
	if PauseBackgroundUpdates(Status{Known: false, OnBattery: true}) {
		t.Fatal("PauseBackgroundUpdates returned true for unknown status")
	}
}
