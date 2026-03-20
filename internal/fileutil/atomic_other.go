//go:build !windows

// Package fileutil предоставляет утилиты для безопасной работы с файлами.
package fileutil

import (
	"fmt"
	"io/fs"
	"os"
)

// WriteAtomic на не-Windows платформах использует os.Rename,
// которая атомарна на POSIX (вызывает rename(2) syscall).
// os.CreateTemp генерирует уникальное имя без math/rand — нет предупреждений Semgrep.
func WriteAtomic(dst string, data []byte, perm fs.FileMode) error {
	dir := "."
	if idx := len(dst) - 1; idx >= 0 {
		for i := len(dst) - 1; i >= 0; i-- {
			if dst[i] == '/' || dst[i] == '\\' {
				dir = dst[:i]
				break
			}
		}
	}
	f, err := os.CreateTemp(dir, ".atomic-*.tmp")
	if err != nil {
		return fmt.Errorf("fileutil.WriteAtomic: create tmp: %w", err)
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("fileutil.WriteAtomic: write tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("fileutil.WriteAtomic: close tmp: %w", err)
	}
	if err := os.Chmod(tmp, perm); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("fileutil.WriteAtomic: chmod: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("fileutil.WriteAtomic: rename: %w", err)
	}
	return nil
}
