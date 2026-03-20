//go:build !windows

// Package fileutil предоставляет утилиты для безопасной работы с файлами.
package fileutil

import (
	"fmt"
	"io/fs"
	"math/rand"
	"os"
)

// WriteAtomic на не-Windows платформах использует os.Rename,
// которая атомарна на POSIX (вызывает rename(2) syscall).
func WriteAtomic(dst string, data []byte, perm fs.FileMode) error {
	tmp := fmt.Sprintf("%s.%d.tmp", dst, rand.Int63())
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return fmt.Errorf("fileutil.WriteAtomic: write tmp: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("fileutil.WriteAtomic: rename: %w", err)
	}
	return nil
}
