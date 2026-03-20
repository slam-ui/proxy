//go:build windows

// Package fileutil предоставляет утилиты для безопасной работы с файлами.
package fileutil

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32dll = windows.NewLazyDLL("kernel32.dll")
	moveFileExW = kernel32dll.NewProc("MoveFileExW")
)

const moveFileReplaceExisting = 0x1

// WriteAtomic атомарно записывает data в dst.
//
// OPT #4: заменили rand.Int63() на os.CreateTemp():
//   - os.CreateTemp использует криптографически стойкий источник имён — нет коллизий
//     при высокой частоте записи из нескольких горутин.
//   - Возвращает уже открытый *os.File — один syscall open вместо двух
//     (os.WriteFile открывал бы файл заново).
//   - Убрали import "math/rand".
//
// MoveFileExW с MOVEFILE_REPLACE_EXISTING атомарна на NTFS —
// меняет directory entry одной транзакцией без промежуточного удаления.
func WriteAtomic(dst string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(dst)
	base := filepath.Base(dst)

	// CreateTemp создаёт файл вида "<base>.*.tmp" с уникальным суффиксом.
	f, err := os.CreateTemp(dir, base+".*.tmp")
	if err != nil {
		return fmt.Errorf("fileutil.WriteAtomic: create tmp: %w", err)
	}
	tmp := f.Name()

	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("fileutil.WriteAtomic: write tmp: %w", writeErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("fileutil.WriteAtomic: close tmp: %w", closeErr)
	}

	// Применяем запрошенные права доступа к временному файлу.
	if err := os.Chmod(tmp, perm); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("fileutil.WriteAtomic: chmod: %w", err)
	}

	dstPtr, err := windows.UTF16PtrFromString(dst)
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("fileutil.WriteAtomic: encode dst: %w", err)
	}
	tmpPtr, err := windows.UTF16PtrFromString(tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("fileutil.WriteAtomic: encode tmp: %w", err)
	}
	r, _, lastErr := moveFileExW.Call(
		uintptr(unsafe.Pointer(tmpPtr)),
		uintptr(unsafe.Pointer(dstPtr)),
		moveFileReplaceExisting,
	)
	if r == 0 {
		_ = os.Remove(tmp)
		return fmt.Errorf("fileutil.WriteAtomic: MoveFileExW: %w", lastErr)
	}
	return nil
}
