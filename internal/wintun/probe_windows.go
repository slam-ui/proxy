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
	modwintun          = syscall.NewLazyDLL("wintun.dll")
	procOpenAdapter    = modwintun.NewProc("WintunOpenAdapter")
	procCloseAdapter   = modwintun.NewProc("WintunCloseAdapter")
	procDeleteAdapter  = modwintun.NewProc("WintunDeleteAdapter")
)

// kernelObjectFree возвращает true если wintun kernel-объект для ifName свободен.
// Fail-open: если wintun.dll не найдена — возвращает true (не блокируем запуск).
//
// OPT #7: UTF16PtrFromString кэшируется по имени интерфейса — при polling каждые 500мс
// это 180 лишних аллокаций за 90с. Имя интерфейса (tun0) фиксировано на весь сеанс.
var (
	cachedIfName    string
	cachedIfNamePtr *uint16
)

// ForceDeleteAdapter принудительно удаляет TUN-адаптер через WinTun DLL.
// В отличие от Remove-NetAdapter который убирает из Device Manager,
// WintunDeleteAdapter удаляет именно тот kernel-объект который блокирует CreateAdapter.
// Fail-silent: ошибки игнорируются — если не получилось, продолжаем обычную очистку.
func ForceDeleteAdapter(ifName string) {
	if err := modwintun.Load(); err != nil {
		return // wintun.dll не найдена
	}
	ptr, err := syscall.UTF16PtrFromString(ifName)
	if err != nil {
		return
	}
	// Сначала открываем адаптер
	r0, _, _ := procOpenAdapter.Call(uintptr(unsafe.Pointer(ptr)))
	if r0 == 0 {
		return // уже не существует
	}
	// Удаляем: WintunDeleteAdapter(adapter) — освобождает kernel-объект синхронно
	_, _, _ = procDeleteAdapter.Call(r0)
	// Закрываем handle (на случай если Delete не закрыл)
	_, _, _ = procCloseAdapter.Call(r0)
}

func kernelObjectFree(ifName string) bool {
	if err := modwintun.Load(); err != nil {
		return true // wintun.dll не найдена, fail-open
	}

	// Кэшируем конвертацию UTF-16 — ifName никогда не меняется в рамках одного запуска.
	if cachedIfNamePtr == nil || cachedIfName != ifName {
		ptr, err := syscall.UTF16PtrFromString(ifName)
		if err != nil {
			return true
		}
		cachedIfName = ifName
		cachedIfNamePtr = ptr
	}

	r0, _, _ := procOpenAdapter.Call(uintptr(unsafe.Pointer(cachedIfNamePtr)))
	if r0 == 0 {
		// NULL → kernel-объект не существует → свободен
		return true
	}
	// Хэндл открыт → объект жив, закрываем немедленно
	_, _, _ = procCloseAdapter.Call(r0)
	return false
}
