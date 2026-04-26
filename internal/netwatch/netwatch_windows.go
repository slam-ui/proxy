//go:build windows

package netwatch

import (
	"context"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	iphlpapi                    = windows.NewLazySystemDLL("iphlpapi.dll")
	procNotifyIPInterfaceChange = iphlpapi.NewProc("NotifyIpInterfaceChange")
	procCancelMibChangeNotify2  = iphlpapi.NewProc("CancelMibChangeNotify2")
)

func Watch(ctx context.Context, onChange func()) error {
	var handle uintptr
	cb := syscall.NewCallback(func(_, _, _ uintptr) uintptr {
		if onChange != nil {
			go onChange()
		}
		return 0
	})
	r, _, err := procNotifyIPInterfaceChange.Call(
		0,
		cb,
		0,
		1,
		uintptr(unsafe.Pointer(&handle)),
	)
	if r != 0 {
		return fmt.Errorf("NotifyIpInterfaceChange: %w", err)
	}
	<-ctx.Done()
	procCancelMibChangeNotify2.Call(handle)
	runtime.KeepAlive(cb)
	return nil
}
