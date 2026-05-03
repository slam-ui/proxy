//go:build windows

package power

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	acLineOffline = 0
	acLineOnline  = 1
)

var (
	kernel32Power            = windows.NewLazySystemDLL("kernel32.dll")
	procGetSystemPowerStatus = kernel32Power.NewProc("GetSystemPowerStatus")
)

type systemPowerStatus struct {
	ACLineStatus        byte
	BatteryFlag         byte
	BatteryLifePercent  byte
	SystemStatusFlag    byte
	BatteryLifeTime     uint32
	BatteryFullLifeTime uint32
}

func Current() (Status, error) {
	var raw systemPowerStatus
	ret, _, err := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&raw)))
	runtime.KeepAlive(&raw)
	if ret == 0 {
		return Status{}, fmt.Errorf("GetSystemPowerStatus: %w", err)
	}
	return statusFromSystem(raw), nil
}

func statusFromSystem(raw systemPowerStatus) Status {
	status := Status{Known: true, BatteryPercent: int(raw.BatteryLifePercent)}
	if raw.BatteryLifePercent == 255 {
		status.BatteryPercent = -1
	}
	status.OnBattery = raw.ACLineStatus == acLineOffline
	if raw.ACLineStatus != acLineOffline && raw.ACLineStatus != acLineOnline {
		status.Known = false
	}
	return status
}
