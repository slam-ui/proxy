package update

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// LaunchInstaller starts proxy-updater.exe next to the current executable.
func (u *Updater) LaunchInstaller(ctx context.Context, downloadedPath string, restartArgs []string) error {
	if downloadedPath == "" {
		return fmt.Errorf("downloaded path is required")
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return fmt.Errorf("resolve executable absolute path: %w", err)
	}
	helper := filepath.Join(filepath.Dir(exe), "proxy-updater.exe")
	if _, err := os.Stat(helper); err != nil {
		return fmt.Errorf("proxy-updater.exe not found: %w", err)
	}
	args := []string{
		"-pid", strconv.Itoa(os.Getpid()),
		"-old", exe,
		"-new", downloadedPath,
	}
	if len(restartArgs) > 0 {
		args = append(args, "-args", joinArgs(restartArgs))
	}
	cmd := exec.CommandContext(ctx, helper, args...)
	cmd.Dir = filepath.Dir(exe)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start proxy-updater.exe: %w", err)
	}
	return nil
}

func joinArgs(args []string) string {
	out := ""
	for _, arg := range args {
		if arg == "" {
			continue
		}
		if out != "" {
			out += " "
		}
		out += arg
	}
	return out
}
