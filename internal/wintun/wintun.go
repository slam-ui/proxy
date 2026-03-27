//go:build windows

// Package wintun управляет жизненным циклом wintun kernel-объекта на Windows.
package wintun

import (
	"context"
	"os"
	"os/exec"
	"proxyclient/internal/logger"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	FastDeleteFile    = "wintun_fast_delete"
	StopFile          = "wintun_stopped_at"
	CleanShutdownFile = "wintun_clean_shutdown"
	gapFile           = "wintun_gap_ns"
	tunInterfaceName  = "tun0"
)

// scErrRe извлекает числовой код ошибки из вывода sc.exe
var scErrRe = regexp.MustCompile(`\b(\d{4,5})\b`)

// CleanSCOutput очищает вывод sc.exe. Экспортирован для тестов.
func CleanSCOutput(raw string) string {
	if utf8.ValidString(raw) {
		trimmed := strings.TrimSpace(strings.ReplaceAll(raw, "\r", ""))
		var lines []string
		for _, line := range strings.Split(trimmed, "\n") {
			if strings.Contains(line, "ERROR:") {
				idx := strings.Index(line, "ERROR:")
				lines = append(lines, strings.TrimSpace(line[idx+6:]))
			} else if line != "" && !strings.Contains(strings.ToUpper(line), "SUCCESS") {
				lines = append(lines, strings.TrimSpace(line))
			}
		}
		if len(lines) > 0 {
			return strings.Join(lines, "\n")
		}
		return trimmed
	}
	if m := scErrRe.FindString(raw); m != "" {
		return "error " + m
	}
	return "запрос не принят"
}

// ── Запись состояния ─────────────────────────────────────────────────────────

func RecordStop(path ...string) {
	stopPath := StopFile
	if len(path) > 0 && path[0] != "" {
		stopPath = path[0]
	}
	data := []byte(strconv.FormatInt(time.Now().UnixNano(), 10))
	_ = os.WriteFile(stopPath, data, 0644)
	_ = os.Remove(CleanShutdownFile)
}

func RecordCleanShutdown(path ...string) {
	cleanPath := CleanShutdownFile
	if len(path) > 0 && path[0] != "" {
		cleanPath = path[0]
	}
	_ = os.WriteFile(cleanPath, []byte("1"), 0644)
}

// ── Адаптивный gap ────────────────────────────────────────────────────────────

const (
	minGapBase         = 60 * time.Second
	minGapMax          = 3 * time.Minute
	coldStartThreshold = 2 * time.Minute
	settleDelayBase    = 5 * time.Second
	settleDelayMax     = 20 * time.Second
)

const (
	MinGapBase     = minGapBase
	MaxGap         = minGapMax
	MinSettleDelay = settleDelayBase
	MaxSettleDelay = settleDelayMax
)

func ReadAdaptiveGap(path ...string) time.Duration {
	file := gapFile
	if len(path) > 0 && path[0] != "" {
		file = path[0]
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return minGapBase
	}
	raw := strings.TrimSpace(string(data))
	if d, err := time.ParseDuration(raw); err == nil {
		return clampGap(d)
	}
	ns, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || ns <= 0 {
		return minGapBase
	}
	return clampGap(time.Duration(ns))
}

func clampGap(d time.Duration) time.Duration {
	if d < minGapBase {
		return minGapBase
	}
	if d > minGapMax {
		return minGapMax
	}
	return d
}

func IncreaseAdaptiveGap(args ...interface{}) time.Duration {
	file := gapFile
	var log logger.Logger
	for _, a := range args {
		switch v := a.(type) {
		case string:
			file = v
		case logger.Logger:
			log = v
		}
	}
	current := ReadAdaptiveGap(file)
	next := current * 2
	if next > minGapMax {
		next = minGapMax
	}
	_ = os.WriteFile(file, []byte(strconv.FormatInt(int64(next), 10)), 0644)
	if log != nil {
		log.Info("wintun: адаптивный gap увеличен до %v", next.Round(time.Second))
	}
	return next
}

func ResetAdaptiveGap(path ...string) {
	file := gapFile
	if len(path) > 0 && path[0] != "" {
		file = path[0]
	}
	_ = os.Remove(file)
}

// ── Расчет времени готовности (ETA) ───────────────────────────────────────────

var estimateCache struct {
	mu        sync.Mutex
	result    time.Time
	fileMtime time.Time
}

func EstimateReadyAt() time.Time {
	// Маркеры проверяем без кэша
	if _, err := os.Stat(CleanShutdownFile); err == nil {
		return time.Now()
	}
	if _, err := os.Stat(FastDeleteFile); err == nil {
		return time.Now().Add(8 * time.Second)
	}

	estimateCache.mu.Lock()
	defer estimateCache.mu.Unlock()

	fi, err := os.Stat(StopFile)
	if err != nil {
		return time.Now()
	}

	mtime := fi.ModTime()
	if mtime.Equal(estimateCache.fileMtime) {
		return estimateCache.result
	}

	result := EstimateReadyAtWithFiles(StopFile, CleanShutdownFile, gapFile)
	estimateCache.result = result
	estimateCache.fileMtime = mtime
	return result
}

// EstimateReadyAtWithFiles — версия для тестов (строго 3 аргумента).
// ФИКС: Исправлена логика возврата времени для уже прошедших событий.
func EstimateReadyAtWithFiles(stopFile, cleanFile, gapFilePath string) time.Time {
	if _, err := os.Stat(cleanFile); err == nil {
		return time.Now()
	}

	data, err := os.ReadFile(stopFile)
	if err != nil {
		return time.Now()
	}

	ns, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return time.Now()
	}

	stopTime := time.Unix(0, ns)
	elapsed := time.Since(stopTime)

	// Если прошло больше порога "холодного старта" — готов сразу
	if elapsed > coldStartThreshold {
		return time.Now()
	}

	gap := ReadAdaptiveGap(gapFilePath)
	readyAt := stopTime.Add(gap)

	// ФИКС: Если расчетное время готовности уже в прошлом — возвращаем "сейчас"
	if readyAt.Before(time.Now()) {
		return time.Now()
	}

	return readyAt
}

// ── Polling Логика ────────────────────────────────────────────────────────────

func PollUntilFree(ctx context.Context, log logger.Logger, ifName string) {
	if _, err := os.Stat(StopFile); os.IsNotExist(err) {
		return
	}

	if _, err := os.Stat(CleanShutdownFile); err == nil {
		_ = os.Remove(CleanShutdownFile)
		log.Info("wintun: чистое завершение — старт без задержек")
		return
	}

	fastDeleted := false
	if _, err := os.Stat(FastDeleteFile); err == nil {
		_ = os.Remove(FastDeleteFile)
		fastDeleted = true
	}

	if !fastDeleted {
		eta := EstimateReadyAt()
		if remaining := time.Until(eta); remaining > 0 {
			log.Info("wintun: ожидание GC Windows до %v...", eta.Format("15:04:05"))
			if !SleepCtx(ctx, remaining) {
				return
			}
		}
	}

	const confirmInterval = 500 * time.Millisecond
	const confirmRequired = 3
	confirmCount := 0
	deadline := time.Now().Add(60 * time.Second)

	for time.Now().Before(deadline) {
		if kernelObjectFree(ifName) && !InterfaceExists(ifName) {
			confirmCount++
			if confirmCount >= confirmRequired {
				settle := ReadSettleDelay()
				SleepCtx(ctx, settle)
				return
			}
		} else {
			confirmCount = 0
		}
		if !SleepCtx(ctx, confirmInterval) {
			return
		}
	}
}

// PollUntilFreeWithFiles — восстановлен для тестов (строго 3 аргумента).
func PollUntilFreeWithFiles(ctx context.Context, stopFile, cleanFile, gapFilePath string) error {
	if _, err := os.Stat(stopFile); os.IsNotExist(err) {
		return nil
	}
	if _, err := os.Stat(cleanFile); err == nil {
		_ = os.Remove(cleanFile)
		return nil
	}

	gap := ReadAdaptiveGap(gapFilePath)
	data, _ := os.ReadFile(stopFile)
	ns, _ := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	stopTime := time.Unix(0, ns)

	if time.Since(stopTime) <= coldStartThreshold {
		remaining := gap - time.Since(stopTime)
		if remaining > 0 {
			if !SleepCtx(ctx, remaining) {
				return ctx.Err()
			}
		}
	}

	if !SleepCtx(ctx, 1500*time.Millisecond) {
		return ctx.Err()
	}
	return nil
}

func ReadSettleDelay(gapFilePath ...string) time.Duration {
	gap := ReadAdaptiveGap(gapFilePath...)
	d := time.Duration(float64(gap) * 0.25)
	if d < settleDelayBase {
		d = settleDelayBase
	}
	if d > settleDelayMax {
		d = settleDelayMax
	}
	return d
}

func SleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

// ── Системные функции ────────────────────────────────────────────────────────

func InterfaceExists(ifName string) bool {
	cmd := exec.Command("netsh", "interface", "show", "interface", ifName)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
	out, err := cmd.CombinedOutput()
	return err == nil && strings.Contains(string(out), ifName)
}

func NetAdapterExists(ifName string) bool {
	cmd := exec.Command("powershell", "-WindowStyle", "Hidden", "-NonInteractive", "-Command",
		`(Get-PnpDevice | Where-Object { $_.FriendlyName -like '*`+ifName+`*' -or $_.FriendlyName -like '*wintun*' } | Measure-Object).Count -gt 0`)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
	out, err := cmd.CombinedOutput()
	return err == nil && strings.TrimSpace(string(out)) == "True"
}

func RemoveStaleTunAdapter(log logger.Logger) {
	repairStaleDriver(log)
	run := func(n string, a ...string) {
		c := exec.Command(n, a...)
		c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
		_ = c.Run()
	}
	run("taskkill", "/F", "/IM", "sing-box.exe", "/T")
	run("sc", "stop", "wintun")

	if ForceDeleteAdapter(tunInterfaceName) {
		_ = os.WriteFile(FastDeleteFile, []byte("1"), 0644)
	}
}

func repairStaleDriver(log logger.Logger) {
	cmd := exec.Command("sc", "query", "wintun")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
	out, _ := cmd.CombinedOutput()
	if strings.Contains(string(out), "STATE") && !strings.Contains(string(out), "RUNNING") {
		_ = exec.Command("sc", "delete", "wintun").Run()
		log.Info("wintun: stale driver registration removed")
	}
}

func Shutdown(log logger.Logger) {
	run := func(n string, a ...string) {
		c := exec.Command(n, a...)
		c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
		_ = c.Run()
	}
	run("sc", "stop", "wintun")
	run("netsh", "interface", "ip", "delete", "interface", tunInterfaceName)
	log.Info("TUN shutdown sent")
}
