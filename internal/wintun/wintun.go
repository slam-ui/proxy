//go:build windows

// Package wintun управляет жизненным циклом wintun kernel-объекта на Windows.
package wintun

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
	minGapBase         = 15 * time.Second
	minGapMax          = 3 * time.Minute
	coldStartThreshold = 2 * time.Minute
	settleDelayBase    = 10 * time.Second
	settleDelayMax     = 20 * time.Second
	// fastDeleteSettle — settle delay для пути ForceDeleteAdapter (CM_Request_Device_Eject).
	// Явное удаление device node через PnP manager чище чем ожидание GC Windows,
	// поэтому 3с достаточно вместо стандартных 15с.
	fastDeleteSettle = 3 * time.Second
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
	mu            sync.Mutex
	result        time.Time
	stopFileMtime time.Time
	gapFileMtime  time.Time
	// stopFilePath хранит абсолютный путь — без него тесты в разных temp-директориях
	// с одинаковым mtime (Windows mtime имеет разрешение 1 сек) дают ложный cache-hit.
	stopFilePath string
	gapFilePath  string
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

	absStopPath, err := filepath.Abs(StopFile)
	if err != nil {
		absStopPath = StopFile
	}

	absGapPath, err := filepath.Abs(gapFile)
	if err != nil {
		absGapPath = gapFile
	}

	gapInfo, err := os.Stat(absGapPath)
	var gapMtime time.Time
	if err == nil {
		gapMtime = gapInfo.ModTime()
	}

	mtime := fi.ModTime()
	if mtime.Equal(estimateCache.stopFileMtime) && absStopPath == estimateCache.stopFilePath &&
		absGapPath == estimateCache.gapFilePath && gapMtime.Equal(estimateCache.gapFileMtime) {
		return estimateCache.result
	}

	result := EstimateReadyAtWithFiles(StopFile, CleanShutdownFile, gapFile)
	estimateCache.result = result
	estimateCache.stopFileMtime = mtime
	estimateCache.stopFilePath = absStopPath
	estimateCache.gapFileMtime = gapMtime
	estimateCache.gapFilePath = absGapPath
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
	now := time.Now()
	if stopTime.After(now.Add(24*time.Hour)) || stopTime.Before(now.Add(-7*24*time.Hour)) {
		// повреждённый timestamp — вне разумного диапазона
		_ = os.Remove(stopFile)
		return now
	}
	elapsed := now.Sub(stopTime)

	// Если прошло больше порога "холодного старта" — готов сразу
	if elapsed > coldStartThreshold {
		return time.Now()
	}

	gap := ReadAdaptiveGap(gapFilePath)
	readyAt := stopTime.Add(gap)

	// ФИКС: Если расчетное время готовности уже в прошлом — возвращаем "сейчас"
	if readyAt.Before(now) {
		return time.Now()
	}

	return readyAt
}

// ── Polling Логика ────────────────────────────────────────────────────────────

func PollUntilFree(ctx context.Context, log logger.Logger, ifName string) {
	// BUG FIX: раньше при отсутствии стоп-файла функция делала немедленный return.
	// Если предыдущая сессия завершилась аварийно (kill, BSOD, power loss) —
	// стоп-файл не записывается, но wintun kernel-объект всё ещё удерживается
	// Windows GC. sing-box падает через 15с с «Cannot create a file when that
	// file already exists». Теперь: даже без стоп-файла проверяем kernel-объект
	// напрямую и ждём его освобождения если он занят.
	noStopFile := false
	if _, err := os.Stat(StopFile); os.IsNotExist(err) {
		// Стоп-файла нет, но kernel-объект может быть жив после аварийного завершения.
		// Быстрая проверка: если объект уже свободен — стартуем сразу (hot path).
		//
		// BUG FIX: используем короткий дочерний контекст (100ms) вместо
		// InterfaceExists (own 300ms ctx) или InterfaceExistsCtx(ctx) (может быть unbounded).
		//
		// Проблема 1 (TestMarkerCombinations/combo_no_stop_ever):
		//   InterfaceExists создавала независимый ctx с 300ms — оба таймера
		//   (внешний и внутренний) истекали почти одновременно → false positive.
		//
		// Проблема 2 (TestPollUntilFree_NoStopFile*):
		//   Передача сырого ctx (t.Context(), без дедлайна) в InterfaceExistsCtx
		//   делала проверку unbounded — медленный netsh блокировал на минуты.
		//
		// Решение: дочерний ctx наследует отмену родителя И ограничен 100ms.
		// При нормальной работе netsh отвечает за <5ms; 100ms — жёсткий потолок.
		fastCtx, fastCancel := context.WithTimeout(ctx, 100*time.Millisecond)
		fastFree := kernelObjectFree(ifName) && !InterfaceExistsCtx(fastCtx, ifName)
		fastCancel()
		if fastFree {
			return
		}
		// Объект ещё жив — упали без стоп-файла, входим в polling.
		log.Info("wintun: стоп-файл отсутствует, но kernel-объект занят (аварийное завершение) — ждём освобождения")
		noStopFile = true
	}

	if !noStopFile {
		if _, err := os.Stat(CleanShutdownFile); err == nil {
			// Удаляем маркер сразу, чтобы EstimateReadyAt не вернул "now" если мы
			// провалимся в gap-ожидание ниже (в случае неудачи ForceDeleteAdapter).
			_ = os.Remove(CleanShutdownFile)
			log.Info("wintun: clean shutdown обнаружен, форсированное удаление адаптера")
			if ForceDeleteAdapter(ifName) {
				// PnP-эжекция прошла успешно: device node удалён корректно,
				// Windows GC не нужен. Используем fastDeleteSettle (3с) вместо gap.
				log.Info("wintun: адаптер удалён через PnP, ждём settle %v", fastDeleteSettle)
				SleepCtx(ctx, fastDeleteSettle)
			} else {
				// ForceDeleteAdapter вернул false (DLL не найдена или адаптер уже свободен).
				// CleanShutdown гарантирует что sing-box вызвал WintunCloseAdapter перед выходом
				// → kernel-объект уже освобождён → можно стартовать немедленно без gap.
				log.Info("wintun: ForceDeleteAdapter не доступен, но CleanShutdown гарантирует освобождение адаптера")
			}
			return
		}
	}

	fastDeleted := false
	if !noStopFile {
		if _, err := os.Stat(FastDeleteFile); err == nil {
			_ = os.Remove(FastDeleteFile)
			fastDeleted = true
		}
	}

	if !fastDeleted && !noStopFile {
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
		// BUG FIX (фаззер): используем InterfaceExistsCtx(ctx) вместо InterfaceExists.
		// InterfaceExists создавала собственный context.Background() с 300ms таймаутом
		// независимо от внешнего ctx. При зависании netsh (cmd.Wait блокируется на
		// WaitForSingleObject(INFINITE) несмотря на WaitDelay) PollUntilFree висела
		// вечно, игнорируя отмену ctx. FuzzStopFileContent: EOF. FuzzPollUntilFreeFiles: таймаут.
		if ctx.Err() != nil {
			return
		}
		if kernelObjectFree(ifName) && !NetAdapterExistsCtx(ctx, ifName) {
			confirmCount++
			if confirmCount >= confirmRequired {
				settle := ReadSettleDelay()
				// ОПТИМИЗАЦИЯ: после явного удаления через CM_Request_Device_Eject
				// (ForceDeleteAdapter) device node удалён из PnP реестра корректно —
				// Windows GC не нужно ждать. Используем 3с вместо 15с.
				if fastDeleted {
					settle = fastDeleteSettle
				}
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

// InterfaceExistsCtx проверяет существование сетевого интерфейса используя
// предоставленный ctx в качестве дедлайна для netsh.
// Используйте эту версию когда вызов уже находится внутри ограниченного контекста
// (например, в fast-path PollUntilFree), чтобы избежать конфликта двух 300ms таймеров.
//
// WaitDelay=50ms: когда ctx отменяется, Go посылает TerminateProcess(netsh).
// На Windows TerminateProcess почти мгновенный, но в редких случаях (сетевые блокировки,
// зависший процесс) cmd.Wait() блокируется на WaitForSingleObject(INFINITE) навсегда.
// WaitDelay гарантирует что Wait вернётся не позже чем через 50ms после отмены ctx,
// предотвращая бесконечный hang (воспроизводится в FuzzPollUntilFreeFiles).
func InterfaceExistsCtx(ctx context.Context, ifName string) bool {
	cmd := exec.CommandContext(ctx, "netsh", "interface", "show", "interface", ifName)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
	cmd.WaitDelay = 50 * time.Millisecond
	out, err := cmd.CombinedOutput()
	return err == nil && strings.Contains(string(out), ifName)
}

func InterfaceExists(ifName string) bool {
	// BUG FIX (фаззер): используем короткий таймаут 300ms вместо 5s.
	// 3 подтверждения × 300ms = 900ms — укладывается в тестовые окна.
	// При реальном использовании netsh отвечает за <50ms.
	//
	// NOTE: не используйте эту функцию в fast-path PollUntilFree — там
	// вызывайте InterfaceExistsCtx(ctx, ifName) чтобы избежать коллизии
	// двух независимых 300ms таймеров (баг: elapsed=320ms в no_stop_ever).
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	return InterfaceExistsCtx(ctx, ifName)
}

func NetAdapterExists(ifName string) bool {
	cmd := exec.Command("powershell", "-WindowStyle", "Hidden", "-NonInteractive", "-Command",
		`(Get-PnpDevice | Where-Object { $_.FriendlyName -like '*`+ifName+`*' -or $_.FriendlyName -like '*wintun*' } | Measure-Object).Count -gt 0`)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
	out, err := cmd.CombinedOutput()
	return err == nil && strings.TrimSpace(string(out)) == "True"
}

func NetAdapterExistsCtx(ctx context.Context, ifName string) bool {
	cmd := exec.CommandContext(ctx, "powershell", "-WindowStyle", "Hidden", "-NonInteractive", "-Command",
		`(Get-PnpDevice | Where-Object { $_.FriendlyName -like '*`+ifName+`*' -or $_.FriendlyName -like '*wintun*' } | Measure-Object).Count -gt 0`)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
	out, err := cmd.CombinedOutput()
	return err == nil && strings.TrimSpace(string(out)) == "True"
}

func RemoveStaleTunAdapter(log logger.Logger) {
	RemoveStaleTunAdapterCtx(context.Background(), log)
}

func RemoveStaleTunAdapterCtx(ctx context.Context, log logger.Logger) {
	repairStaleDriver(ctx, log)
	run := func(n string, a ...string) {
		c := exec.Command(n, a...)
		c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
		_ = c.Run()
	}
	run("taskkill", "/F", "/IM", "sing-box.exe", "/T")

	// BUG FIX: ForceDeleteAdapter вызывается ДО sc stop wintun.
	//
	// Проблема: sc stop выгружает wintun драйвер → WintunOpenAdapter возвращает NULL
	// (невозможно связаться с драйвером, а не "адаптер не существует").
	// ForceDeleteAdapter трактовал NULL как "адаптер свободен" → создавал FastDeleteFile
	// без реального удаления device node → при следующем старте sing-box загружал
	// драйвер, находил stale-регистрацию в реестре → WintunCreateAdapter падал с
	// "Cannot create a file when that file already exists".
	//
	// Исправление: даём Windows 2с освободить хэндлы убитого процесса, затем
	// вызываем ForceDeleteAdapter пока драйвер ещё работает — WintunDeleteAdapter
	// корректно удаляет device node через CM_Request_Device_Eject. sc stop — после.
	time.Sleep(2 * time.Second)
	if ForceDeleteAdapter(tunInterfaceName) {
		_ = os.WriteFile(FastDeleteFile, []byte("1"), 0644)
	}
	run("sc", "stop", "wintun")
	// Дать время драйверу выгрузиться и пересканировать hardware
	time.Sleep(2 * time.Second)
	run("pnputil", "/scan-for-hardware-changes")
}

// repairStaleDriver удаляет регистрацию wintun сервиса только если он завис
// в состоянии STOP_PENDING (SCM не может его остановить корректно).
//
// Намеренно НЕ удаляем при состоянии STOPPED — это нормальное состояние между
// запусками. Удаление при STOPPED вызывало: sc delete → ядро держит driver object
// → WintunCreateAdapter пытается переустановить → таймаут 10с → FATAL (BUG #TURN-1).
//
// При STOPPED достаточно вызвать sc stop (ноп) — wintun.dll при следующем
// WintunCreateAdapter сама разберётся с регистрацией.
func repairStaleDriver(ctx context.Context, log logger.Logger) {
	cmd := exec.CommandContext(ctx, "sc", "query", "wintun")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return // сервис не зарегистрирован — ничего делать не нужно
	}
	// Только STOP_PENDING — сервис завис и SCM не смог его остановить.
	// Удаляем регистрацию, чтобы следующий WintunCreateAdapter начал с чистого листа.
	if !strings.Contains(string(out), "STOP_PENDING") {
		return
	}
	_ = exec.CommandContext(ctx, "sc", "delete", "wintun").Run()
	log.Info("wintun: stale driver registration removed (was STOP_PENDING)")
	// Ждём пока SCM подтвердит удаление (обычно < 1с).
	for i := 0; i < 10; i++ {
		if !SleepCtx(ctx, 300*time.Millisecond) {
			return // отменено при shutdown
		}
		chk := exec.CommandContext(ctx, "sc", "query", "wintun")
		chk.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
		if _, e := chk.Output(); e != nil {
			break // сервис исчез из SCM
		}
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
