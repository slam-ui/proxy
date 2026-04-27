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
	procEmptyClipboard = user32.NewProc("EmptyClipboard")
	procSetClipboard   = user32.NewProc("SetClipboardData")
	kernel32           = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalAlloc    = kernel32.NewProc("GlobalAlloc")
	procGlobalFree     = kernel32.NewProc("GlobalFree")
	procGlobalLock     = kernel32.NewProc("GlobalLock")
	procGlobalUnlock   = kernel32.NewProc("GlobalUnlock")
	procRtlMoveMemory  = kernel32.NewProc("RtlMoveMemory")
)

const (
	cfUnicodeText = 13
	gmemMoveable  = 0x0002
)

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

func Write(text string) bool {
	data, err := windows.UTF16FromString(text)
	if err != nil {
		return false
	}
	r, _, _ := procOpenClipboard.Call(0)
	if r == 0 {
		return false
	}
	defer procCloseClipboard.Call()
	if r, _, _ := procEmptyClipboard.Call(); r == 0 {
		return false
	}
	size := uintptr(len(data) * 2)
	h, _, _ := procGlobalAlloc.Call(gmemMoveable, size)
	if h == 0 {
		return false
	}
	ptr, _, _ := procGlobalLock.Call(h)
	if ptr == 0 {
		procGlobalFree.Call(h)
		return false
	}
	procRtlMoveMemory.Call(ptr, uintptr(unsafe.Pointer(&data[0])), size)
	runtime.KeepAlive(data)
	procGlobalUnlock.Call(h)
	r, _, _ = procSetClipboard.Call(cfUnicodeText, h)
	if r == 0 {
		procGlobalFree.Call(h)
		return false
	}
	return true
}

func ptrAt[T any](addr uintptr) *T {
	return (*T)(unsafe.Add(unsafe.Pointer(nil), addr))
}
