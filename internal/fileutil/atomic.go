//go:build windows

// Package fileutil предоставляет утилиты для безопасной работы с файлами.
package fileutil

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32dll = windows.NewLazyDLL("kernel32.dll")
	moveFileExW = kernel32dll.NewProc("MoveFileExW")
)

const moveFileReplaceExisting = 0x1

// WriteAtomic атомарно записывает data в dst.
func WriteAtomic(dst string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(dst)
	base := filepath.Base(dst)

	// CreateTemp создаёт файл с уникальным именем, что исключает коллизии.
	f, err := os.CreateTemp(dir, base+".*.tmp")
	if err != nil {
		return fmt.Errorf("fileutil.WriteAtomic: create tmp: %w", err)
	}
	tmp := f.Name()

	// Гарантируем очистку временного файла в случае паники или раннего выхода.
	// Если всё пройдет успешно, мы вызовем Remove после MoveFileExW (хотя Move его и так поглотит).
	defer func() {
		if _, err := os.Stat(tmp); err == nil {
			_ = os.Remove(tmp)
		}
	}()

	_, writeErr := f.Write(data)
	// BUG FIX: fsync перед закрытием — гарантирует физическую запись на диск.
	// Без Sync() при BSOD/power loss файл может оказаться пустым или содержать мусор
	// после атомарного rename. Источник: Tailscale atomicfile, etcd fileutil.
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil {
		return fmt.Errorf("fileutil.WriteAtomic: write tmp: %w", writeErr)
	}
	if syncErr != nil {
		return fmt.Errorf("fileutil.WriteAtomic: sync tmp: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("fileutil.WriteAtomic: close tmp: %w", closeErr)
	}

	// Применяем права доступа.
	if err := os.Chmod(tmp, perm); err != nil {
		return fmt.Errorf("fileutil.WriteAtomic: chmod: %w", err)
	}

	dstPtr, err := windows.UTF16PtrFromString(dst)
	if err != nil {
		return fmt.Errorf("fileutil.WriteAtomic: encode dst: %w", err)
	}
	tmpPtr, err := windows.UTF16PtrFromString(tmp)
	if err != nil {
		return fmt.Errorf("fileutil.WriteAtomic: encode tmp: %w", err)
	}

	// ФИКС: Добавляем цикл повторных попыток для MoveFileExW.
	// На Windows файлы часто блокируются антивирусами или другими потоками на доли секунды.
	const maxRetries = 10
	var lastMoveErr error

	for i := 0; i < maxRetries; i++ {
		r, _, lastErr := moveFileExW.Call(
			uintptr(unsafe.Pointer(tmpPtr)),
			uintptr(unsafe.Pointer(dstPtr)),
			moveFileReplaceExisting,
		)

		if r != 0 {
			// Успешно перемещено!
			return nil
		}

		lastMoveErr = lastErr

		// Проверяем, является ли ошибка временной блокировкой доступа.
		// ERROR_ACCESS_DENIED (5) или ERROR_SHARING_VIOLATION (32).
		if lastErr == windows.ERROR_ACCESS_DENIED || lastErr == windows.ERROR_SHARING_VIOLATION {
			// Пауза перед следующей попыткой (10ms, 20ms, 30ms...)
			time.Sleep(time.Duration(i+1) * 10 * time.Millisecond)
			continue
		}

		// Если ошибка критическая (например, неверный путь), выходим сразу.
		break
	}

	return fmt.Errorf("fileutil.WriteAtomic: MoveFileExW: %w", lastMoveErr)
}
