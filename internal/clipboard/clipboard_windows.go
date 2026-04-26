//go:build windows

package clipboard

import (
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32             = windows.NewLazySystemDLL("user32.dll")
	procOpenClipboard  = user32.NewProc("OpenClipboard")
	procCloseClipboard = user32.NewProc("CloseClipboard")
	procGetClipboard   = user32.NewProc("GetClipboardData")
	kernel32           = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalLock     = kernel32.NewProc("GlobalLock")
	procGlobalUnlock   = kernel32.NewProc("GlobalUnlock")
)

const cfUnicodeText = 13

func Read() string {
	r, _, _ := procOpenClipboard.Call(0)
	if r == 0 {
		return ""
	}
	defer procCloseClipboard.Call()
	h, _, _ := procGetClipboard.Call(cfUnicodeText)
	if h == 0 {
		return ""
	}
	ptr, _, _ := procGlobalLock.Call(h)
	if ptr == 0 {
		return ""
	}
	defer procGlobalUnlock.Call(h)
	text := windows.UTF16PtrToString(ptrAt[uint16](ptr))
	runtime.KeepAlive(h)
	return text
}

func ptrAt[T any](addr uintptr) *T {
	return (*T)(unsafe.Add(unsafe.Pointer(nil), addr))
}
