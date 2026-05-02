//go:build windows

package wintun

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
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
	wintunLoadOnce    sync.Once
	wintunLoadErr     error
	procOpenAdapter   uintptr
	procCloseAdapter  uintptr
	procDeleteAdapter uintptr
)

func loadWintunDLL() error {
	wintunLoadOnce.Do(func() {
		exe, err := os.Executable()
		if err != nil {
			wintunLoadErr = fmt.Errorf("resolve executable path: %w", err)
			return
		}
		dllPath := filepath.Join(filepath.Dir(exe), "wintun.dll")
		h, err := windows.LoadLibraryEx(
			dllPath,
			0,
			windows.LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR|windows.LOAD_LIBRARY_SEARCH_SYSTEM32,
		)
		if err != nil {
			wintunLoadErr = fmt.Errorf("load %s: %w", dllPath, err)
			return
		}
		if procOpenAdapter, err = windows.GetProcAddress(h, "WintunOpenAdapter"); err != nil {
			wintunLoadErr = fmt.Errorf("WintunOpenAdapter: %w", err)
			return
		}
		if procCloseAdapter, err = windows.GetProcAddress(h, "WintunCloseAdapter"); err != nil {
			wintunLoadErr = fmt.Errorf("WintunCloseAdapter: %w", err)
			return
		}
		if procDeleteAdapter, err = windows.GetProcAddress(h, "WintunDeleteAdapter"); err != nil {
			wintunLoadErr = fmt.Errorf("WintunDeleteAdapter: %w", err)
		}
	})
	return wintunLoadErr
}

// kernelObjectFree возвращает true если wintun kernel-объект для ifName свободен.
// Fail-open: если wintun.dll не найдена — возвращает true (не блокируем запуск).
//
// OPT #7: UTF16PtrFromString кэшируется по имени интерфейса — при polling каждые 500мс
// это 180 лишних аллокаций за 90с. Имя интерфейса (tun0) фиксировано на весь сеанс.
// BUG FIX: кэш защищён мьютексом — PollUntilFree может вызываться из нескольких горутин
// одновременно (handleCrash + BeforeRestart), что давало data race на cachedIfName/Ptr.
var (
	cachedMu        sync.Mutex
	cachedIfName    string
	cachedIfNamePtr *uint16
)

// ForceDeleteAdapter принудительно удаляет TUN-адаптер через WinTun DLL.
// В отличие от Remove-NetAdapter который убирает из Device Manager,
// WintunDeleteAdapter удаляет именно тот kernel-объект который блокирует CreateAdapter.
// Возвращает true если WintunDeleteAdapter выполнился успешно — в этом случае
// kernel-объект освобождён синхронно и PollUntilFree может пропустить длинный gap.
// Возвращает false если DLL не найдена, адаптер не существовал, или Delete вернул ошибку.
func ForceDeleteAdapter(ctx context.Context, ifName string) bool {
	if err := loadWintunDLL(); err != nil {
		return false // wintun.dll не найдена
	}
	ptr, err := syscall.UTF16PtrFromString(ifName)
	if err != nil {
		return false
	}
	defer runtime.KeepAlive(ptr)

	// Сначала открываем адаптер
	r0, _, _ := syscall.SyscallN(procOpenAdapter, uintptr(unsafe.Pointer(ptr)))
	if r0 == 0 {
		// NULL на первый вызов может означать:
		// 1. Адаптер не существует (успех)
		// 2. wintun драйвер не загруженный (ложный успех)
		// БАГ #1: раньше мы предполагали успех, но на самом деле нужна проверка.
		//
		// Решение: попробуем открыть адаптер ещё раз после небольшой задержки.
		// Если по-прежнему NULL — адаптер действительно не существует.
		// Если во второй раз получили хэндл — драйвер заработал, но адаптер не был удалён.
		// SleepCtx прерывается при отмене контекста — не блокируем shutdown.
		if !SleepCtx(ctx, 100*time.Millisecond) {
			return false
		}
		r0, _, _ = syscall.SyscallN(procOpenAdapter, uintptr(unsafe.Pointer(ptr)))
		if r0 == 0 {
			// По-прежнему не открывается → адаптер точно не существует
			return true
		}
		// Во второй раз открылось → адаптер жив, нужно его удалить.
		// Продолжаем ниже с полученным хэндлом.
	}

	// Удаляем: WintunDeleteAdapter(adapter) — освобождает kernel-объект синхронно
	r1, _, _ := syscall.SyscallN(procDeleteAdapter, r0)
	// Закрываем handle (на случай если Delete не закрыл)
	_, _, _ = syscall.SyscallN(procCloseAdapter, r0)

	// WintunDeleteAdapter возвращает TRUE (non-zero) при успехе
	// БАГ #1: добавим финальную проверку что адаптер действительно удалён
	if r1 == 0 {
		return false // удаление не удалось
	}

	// Финальная верификация: убедимся что адаптер действительно больше не существует
	// SleepCtx прерывается при отмене контекста — не блокируем shutdown.
	if !SleepCtx(ctx, 100*time.Millisecond) {
		return false
	}
	r2, _, _ := syscall.SyscallN(procOpenAdapter, uintptr(unsafe.Pointer(ptr)))
	if r2 != 0 {
		// Адаптер всё ещё существует после "удаления" — не удалось
		_, _, _ = syscall.SyscallN(procCloseAdapter, r2)
		return false
	}

	return true
}

func kernelObjectFree(ifName string) bool {
	if err := loadWintunDLL(); err != nil {
		return true // wintun.dll не найдена, fail-open
	}

	// BUG FIX: захватываем мьютекс перед работой с кэшем.
	// kernelObjectFree вызывается из PollUntilFree который может запускаться
	// в нескольких горутинах одновременно — без мьютекса data race на cachedIfName/Ptr.
	cachedMu.Lock()
	if cachedIfNamePtr == nil || cachedIfName != ifName {
		ptr, err := syscall.UTF16PtrFromString(ifName)
		if err != nil {
			cachedMu.Unlock()
			return true
		}
		cachedIfName = ifName
		cachedIfNamePtr = ptr
	}
	ptr := cachedIfNamePtr
	cachedMu.Unlock()

	r0, _, _ := syscall.SyscallN(procOpenAdapter, uintptr(unsafe.Pointer(ptr)))
	runtime.KeepAlive(ptr)
	if r0 == 0 {
		// NULL → kernel-объект не существует → свободен
		return true
	}
	// Хэндл открыт → объект жив, закрываем немедленно
	_, _, _ = syscall.SyscallN(procCloseAdapter, r0)
	return false
}
