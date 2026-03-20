//go:build windows

package wintun

import (
	"syscall"
	"unsafe"
)

// ── Прямое зондирование kernel-объекта через wintun.dll ──────────────────────
//
// Вынесено в отдельный файл с тегом //go:build windows:
//   - Тестовые бинарники на Linux/CI не тащат syscall к wintun.dll.
//   - Windows Defender не блокирует wintun.test.exe при сборке на Windows
//     (DLL-зависимость только в финальном бинарнике, не в тестах).
//
// WintunOpenAdapter возвращает NULL если kernel-объект \Device\WINTUN-{GUID}
// не существует — прямая проверка без угадывания через временной gap.

var (
	modwintun        = syscall.NewLazyDLL("wintun.dll")
	procOpenAdapter  = modwintun.NewProc("WintunOpenAdapter")
	procCloseAdapter = modwintun.NewProc("WintunCloseAdapter")
)

// kernelObjectFree возвращает true если wintun kernel-объект для ifName свободен.
// Fail-open: если wintun.dll не найдена — возвращает true (не блокируем запуск).
func kernelObjectFree(ifName string) bool {
	if err := modwintun.Load(); err != nil {
		return true // wintun.dll не найдена, fail-open
	}
	name16, err := syscall.UTF16PtrFromString(ifName)
	if err != nil {
		return true
	}
	r0, _, _ := procOpenAdapter.Call(uintptr(unsafe.Pointer(name16)))
	if r0 == 0 {
		// NULL → kernel-объект не существует → свободен
		return true
	}
	// Хэндл открыт → объект жив, закрываем немедленно
	_, _, _ = procCloseAdapter.Call(r0)
	return false
}
