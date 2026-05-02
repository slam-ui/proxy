//go:build windows

package xray

import (
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	psapi                    = windows.NewLazySystemDLL("psapi.dll")
	procGetProcessMemoryInfo = psapi.NewProc("GetProcessMemoryInfo")
)

type processMemoryCounters struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

func (m *manager) MemoryMB() uint64 {
	pid := m.GetPID()
	if pid == 0 {
		return 0
	}
	if pid < 0 || uint64(pid) > uint64(^uint32(0)) {
		return 0
	}
	pid32 := uint32(pid) // #nosec G115 -- pid is bounded by the check above.
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ, false, pid32)
	if err != nil {
		return 0
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var counters processMemoryCounters
	counters.CB = uint32(unsafe.Sizeof(counters))
	r, _, _ := procGetProcessMemoryInfo.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(&counters)),
		uintptr(unsafe.Sizeof(counters)),
	)
	runtime.KeepAlive(counters)
	if r == 0 {
		return 0
	}
	return uint64(counters.WorkingSetSize) / 1024 / 1024
}
