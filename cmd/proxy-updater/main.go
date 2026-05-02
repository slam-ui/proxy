package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	waitTimeout  = 30 * time.Second
	retryDelay   = 200 * time.Millisecond
	replaceTries = 75
	backupSuffix = ".bak"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("proxy-updater", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	pid := fs.Int("pid", 0, "parent process PID")
	oldPath := fs.String("old", "", "current executable path")
	newPath := fs.String("new", "", "downloaded executable path")
	restartArgs := fs.String("args", "", "arguments for restarting the app")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *oldPath == "" || *newPath == "" {
		return fmt.Errorf("-old and -new are required")
	}
	oldAbs, err := filepath.Abs(*oldPath)
	if err != nil {
		return fmt.Errorf("resolve old path: %w", err)
	}
	newAbs, err := filepath.Abs(*newPath)
	if err != nil {
		return fmt.Errorf("resolve new path: %w", err)
	}
	if *pid > 0 {
		if err := waitForExit(*pid, waitTimeout); err != nil {
			return err
		}
	}
	backup := oldAbs + backupSuffix
	if err := copyWithRetry(oldAbs, backup); err != nil {
		return fmt.Errorf("backup current executable: %w", err)
	}
	if err := copyWithRetry(newAbs, oldAbs); err != nil {
		_ = copyWithRetry(backup, oldAbs)
		return fmt.Errorf("replace executable: %w", err)
	}
	if err := startApp(oldAbs, splitArgs(*restartArgs)); err != nil {
		return fmt.Errorf("restart app: %w", err)
	}
	return nil
}

func waitForExit(pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		running, err := processRunning(pid)
		if err != nil {
			return err
		}
		if !running {
			return nil
		}
		time.Sleep(retryDelay)
	}
	return killProcess(pid)
}

func processRunning(pid int) (bool, error) {
	if runtime.GOOS == "windows" {
		cmd := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/FO", "CSV", "/NH")
		out, err := cmd.Output()
		if err != nil {
			return false, fmt.Errorf("tasklist pid %d: %w", pid, err)
		}
		return strings.Contains(string(out), strconv.Itoa(pid)), nil
	}
	err := exec.Command("kill", "-0", strconv.Itoa(pid)).Run()
	return err == nil, nil
}

func killProcess(pid int) error {
	if runtime.GOOS == "windows" {
		return exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").Run()
	}
	return exec.Command("kill", "-TERM", strconv.Itoa(pid)).Run()
}

func copyWithRetry(src, dst string) error {
	var lastErr error
	for i := 0; i < replaceTries; i++ {
		if err := copyFile(src, dst); err != nil {
			lastErr = err
			time.Sleep(retryDelay)
			continue
		}
		return nil
	}
	return lastErr
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func startApp(path string, args []string) error {
	cmd := exec.Command(path, args...)
	cmd.Dir = filepath.Dir(path)
	return cmd.Start()
}

func splitArgs(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	return strings.Fields(raw)
}
