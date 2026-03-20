// Package fileutil предоставляет утилиты для безопасной работы с файлами.
package fileutil

import (
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32dll  = syscall.NewLazyDLL("kernel32.dll")
	moveFileExW  = kernel32dll.NewProc("MoveFileExW")
)

const moveFileReplaceExisting = 0x1

// WriteAtomic атомарно записывает data в dst.
//
// Проблема os.Rename на Windows:
//   os.Rename над существующим файлом НЕ атомарна: ядро сначала удаляет
//   целевой файл, потом переименовывает источник. В этом окне конкурирующий
//   writer может вклиниться и два JSON-блока конкатенируются → невалидный файл.
//
// Решение: MoveFileExW с флагом MOVEFILE_REPLACE_EXISTING атомарна на NTFS —
// меняет directory entry одной транзакцией без промежуточного удаления.
//
// Уникальное tmp-имя (rand.Int63) предотвращает гонку нескольких вызывающих
// на одном tmp-файле, что происходит при общем statePath+".tmp".
func WriteAtomic(dst string, data []byte, perm fs.FileMode) error {
	tmp := fmt.Sprintf("%s.%d.tmp", dst, rand.Int63())
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return fmt.Errorf("fileutil.WriteAtomic: write tmp: %w", err)
	}
	dstPtr, err := syscall.UTF16PtrFromString(dst)
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("fileutil.WriteAtomic: encode dst: %w", err)
	}
	tmpPtr, err := syscall.UTF16PtrFromString(tmp)
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
