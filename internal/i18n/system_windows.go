//go:build windows

package i18n

import (
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var kernel32 = windows.NewLazySystemDLL("kernel32.dll")
var procGetUserDefaultLocaleName = kernel32.NewProc("GetUserDefaultLocaleName")

func SystemLocale() Locale {
	var buf [85]uint16
	ret, _, _ := procGetUserDefaultLocaleName.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if ret == 0 {
		return LocaleEN
	}
	name := windows.UTF16ToString(buf[:])
	if strings.HasPrefix(strings.ToLower(name), "ru") {
		return LocaleRU
	}
	return LocaleEN
}
