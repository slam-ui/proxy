//go:build windows

package power

import "testing"

func TestStatusFromSystemPowerStatusBattery(t *testing.T) {
	status := statusFromSystem(systemPowerStatus{ACLineStatus: acLineOffline, BatteryLifePercent: 42})
	if !status.Known || !status.OnBattery || status.BatteryPercent != 42 {
		t.Fatalf("unexpected battery status: %+v", status)
	}
}

func TestStatusFromSystemPowerStatusUnknownPercent(t *testing.T) {
	status := statusFromSystem(systemPowerStatus{ACLineStatus: acLineOnline, BatteryLifePercent: 255})
	if !status.Known || status.OnBattery || status.BatteryPercent != -1 {
		t.Fatalf("unexpected AC status: %+v", status)
	}
}
