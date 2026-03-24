// Package wintun управляет жизненным циклом wintun kernel-объекта на Windows.
//
// Корень проблемы "Cannot create a file when that file already exists":
//
//	sing-box грузит wintun.dll напрямую (не через SCM) и создаёт
//	именованный kernel-объект \Device\WINTUN-{GUID}.
//	После смерти sing-box объект живёт ещё 5-60 секунд (Windows GC асинхронный).
//	sc stop wintun не помогает — драйвер не регистрируется в SCM.
//
// Решение (v3 — DLL probe):
//  1. При каждой остановке sing-box записываем timestamp в файл (RecordStop).
//  2. При старте: Remove-NetAdapter для очистки Device Manager,
//     затем активный polling через WintunOpenAdapter из wintun.dll (probe_windows.go)
//     до реального освобождения kernel-объекта.
//  3. WintunOpenAdapter возвращает NULL → kernel объект свободен → старт.
//     В отличие от netsh, probe работает напрямую с kernel-объектом и даёт
//     точный сигнал без угадывания времени ожидания.
//
// BUG FIX #6: предыдущий комментарий описывал устаревший netsh-подход (v2).
// После рефакторинга PollUntilFree использует DLL probe, netsh не применяется.
//
// Адаптивный gap (v2, сохранён как fallback):
//
//	При каждом wintun-краше gap удваивается (IncreaseAdaptiveGap).
//	При чистом выходе сбрасывается (ResetAdaptiveGap).
//	Холодный старт (StopFile > 5 мин) — gap пропускается полностью.
package wintun

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
	"proxyclient/internal/logger"
)

// FastDeleteFile — маркер успешного WintunDeleteAdapter в RemoveStaleTunAdapter.
// Если файл существует, PollUntilFree пропускает длинный gap (kernel-объект уже
// освобождён синхронно через DLL) и переходит сразу к confirm-loop + settle-delay.
// Файл удаляется в PollUntilFree после чтения — одноразовый сигнал.
// Экспортирован для использования в тестах (аналогично StopFile).
const FastDeleteFile = "wintun_fast_delete"

// tunInterfaceName — имя TUN-адаптера.
// BUG FIX #5: ранее строка "tun0" была захардкожена в нескольких местах.
// Вынесено в константу чтобы при смене имени адаптера достаточно было
// обновить одно место. Должна совпадать с config.TunInterfaceName.
const tunInterfaceName = "tun0"

// scErrRe извлекает числовой код ошибки из вывода sc.exe
var scErrRe = regexp.MustCompile(`\b(\d{4,5})\b`)

// cleanSCOutput очищает вывод sc.exe от кракозябров (Windows-1251 в UTF-8 контексте).
// Вместо кириллического сообщения оставляет только числовой код ошибки.
func cleanSCOutput(raw string) string {
	if utf8.ValidString(raw) {
		return strings.TrimSpace(strings.ReplaceAll(raw, "\r", ""))
	}
	if m := scErrRe.FindString(raw); m != "" {
		return "error " + m
	}
	return "запрос не принят"
}

const (
	StopFile = "wintun_stopped_at" // файл с timestamp последней остановки
	// CleanShutdownFile — маркер корректного завершения sing-box (graceful stop).
	// При наличии этого маркера PollUntilFree знает что wintun был закрыт через
	// WintunCloseAdapter/WintunDeleteAdapter, PnP-запись чистая → gap=0, settle=0.
	// Это самый быстрый путь: следующий старт начнётся без какого-либо ожидания.
	// Файл удаляется в начале PollUntilFree (одноразовый сигнал).
	CleanShutdownFile = "wintun_clean_shutdown"
	// BUG FIX #14: PollInterval и PollTimeout были экспортированы но нигде
	// не использовались снаружи пакета. После рефакторинга на DLL-probe
	// PollUntilFree использует локальные константы probeInterval/probeTimeout.
	// Приватные версии тоже удалены — Go не позволяет иметь неиспользуемые const
	// в test-сборке, и они создавали путаницу со значениями в PollUntilFree.
)

// RecordStop записывает текущее время в файл StopFile.
// Вызывается каждый раз когда sing-box останавливается (kill, graceful, crash).
func RecordStop() {
	data := []byte(strconv.FormatInt(time.Now().UnixNano(), 10))
	_ = os.WriteFile(StopFile, data, 0644)
	// При RecordStop сбрасываем маркер чистого завершения — он будет установлен
	// позже через RecordCleanShutdown если остановка была корректной.
	_ = os.Remove(CleanShutdownFile)
}

// RecordCleanShutdown записывает маркер корректного завершения sing-box.
// Вызывается ТОЛЬКО когда sing-box остановлен штатно через Stop() (CTRL_BREAK → Wait).
// При наличии этого маркера следующий PollUntilFree пропустит все задержки —
// wintun kernel-объект уже освобождён через WintunCloseAdapter, PnP-запись чистая.
// Аналог подхода WireGuard для Windows: корректная остановка = быстрый следующий старт.
func RecordCleanShutdown() {
	_ = os.WriteFile(CleanShutdownFile, []byte("1"), 0644)
}

// ── Адаптивный gap ────────────────────────────────────────────────────────────

// gapFile хранит текущий адаптивный minGap (в наносекундах) между сессиями.
// Значение увеличивается при каждом wintun-краше и сбрасывается при чистом выходе.
const gapFile = "wintun_gap_ns"

// minGapBase — базовый (минимальный) gap без истории крашей.
// 60с: реальный Windows GC занимает 5–30с; 60с даёт достаточный запас
// для большинства систем без накопленных крашей.
// Если 60с окажется мало — адаптивный gap автоматически вырастет через
// IncreaseAdaptiveGap и запомнит это в wintun_gap_ns для следующих стартов.
const minGapBase = 60 * time.Second

// minGapMax — потолок адаптивного gap.
// 3 минуты: gap сбрасывается после каждого успешного старта (ResetAdaptiveGap),
// поэтому он растёт только в рамках одной неудачной сессии.
const minGapMax = 3 * time.Minute

// coldStartThreshold — если StopFile старше этого времени, считаем холодным
// стартом: Windows GC давно отработал, gap не нужен совсем.
// 2 минуты: снижено с 5 минут — Windows GC освобождает wintun объект
// за 5–30с; ждать 5 минут чтобы пропустить gap избыточно.
const coldStartThreshold = 2 * time.Minute

// ReadAdaptiveGap читает текущий адаптивный gap из файла.
// Возвращает minGapBase если файл не найден или повреждён.
func ReadAdaptiveGap() time.Duration {
	data, err := os.ReadFile(gapFile)
	if err != nil {
		return minGapBase
	}
	ns, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || ns <= 0 {
		return minGapBase
	}
	d := time.Duration(ns)
	if d < minGapBase {
		return minGapBase
	}
	if d > minGapMax {
		return minGapMax
	}
	return d
}

// IncreaseAdaptiveGap удваивает адаптивный gap после краша wintun.
// Каждый краш показывает что предыдущего gap не хватило — удваиваем до minGapMax.
// Вызывается из OnCrash при обнаружении wintun-конфликта.
func IncreaseAdaptiveGap(log logger.Logger) {
	current := ReadAdaptiveGap()
	next := current * 2
	if next > minGapMax {
		next = minGapMax
	}
	data := []byte(strconv.FormatInt(int64(next), 10))
	if err := os.WriteFile(gapFile, data, 0644); err == nil {
		log.Info("wintun: адаптивный gap увеличен %v → %v",
			current.Round(time.Second), next.Round(time.Second))
	}
}

// ResetAdaptiveGap сбрасывает gap до базового значения.
// Вызывается при graceful выходе — следующий старт без лишнего ожидания.
func ResetAdaptiveGap() {
	_ = os.Remove(gapFile)
}

// estimateCache кэш для EstimateReadyAt — инвалидируется по mtime StopFile.
// OPT #10: ранее читала wintun_stopped_at с диска при каждом вызове.
// /api/status поллится каждые 2с при warming=true → каждые 2с был syscall ReadFile.
// Инвалидация по mtime: один stat вместо полного ReadFile при каждом poll.
var estimateCache struct {
	mu        sync.Mutex
	result    time.Time
	fileMtime time.Time
}

// EstimateReadyAt возвращает приблизительное время готовности.
// Кэш инвалидируется когда mtime StopFile изменяется (новая остановка sing-box).
//
// Маркерные файлы (CleanShutdownFile, FastDeleteFile) проверяются без кэша —
// они могут появиться/исчезнуть независимо от StopFile, и их изменение должно
// немедленно отражаться в ETA (иначе UI покажет неверное время прогресс-бара).
func EstimateReadyAt() time.Time {
	// Маркеры проверяем без кэша — быстро (только stat, не ReadFile).
	if _, err := os.Stat(CleanShutdownFile); err == nil {
		return time.Now()
	}
	if _, err := os.Stat(FastDeleteFile); err == nil {
		return time.Now().Add(8 * time.Second)
	}

	estimateCache.mu.Lock()
	defer estimateCache.mu.Unlock()

	fi, statErr := os.Stat(StopFile)
	if statErr != nil {
		return time.Now()
	}
	mtime := fi.ModTime()
	if mtime.Equal(estimateCache.fileMtime) {
		return estimateCache.result
	}
	result := computeEstimateReadyAt()
	estimateCache.result = result
	estimateCache.fileMtime = mtime
	return result
}

// computeEstimateReadyAt выполняет фактическое вычисление ETA.
func computeEstimateReadyAt() time.Time {
	// Путь 0: CleanShutdownFile → запуск мгновенный (path 0 в PollUntilFree).
	// UI не должен показывать прогресс-бар — ETA = now.
	if _, err := os.Stat(CleanShutdownFile); err == nil {
		return time.Now()
	}

	// Путь 1: FastDeleteFile → kernel-объект уже освобождён синхронно через DLL.
	// Ожидание: только confirm-loop (1.5с) + settle (5с) ≈ 7с.
	// Для UI это достаточно близко к "сейчас" чтобы не показывать долгий прогресс.
	if _, err := os.Stat(FastDeleteFile); err == nil {
		return time.Now().Add(8 * time.Second)
	}

	data, err := os.ReadFile(StopFile)
	if err != nil {
		return time.Now()
	}
	ns, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return time.Now()
	}
	stopTime := time.Unix(0, ns)
	elapsed := time.Since(stopTime)
	if elapsed > coldStartThreshold {
		return time.Now()
	}
	// Используем полный адаптивный gap как оценку времени готовности.
	// Это соответствует новой логике PollUntilFree которая ждёт full gap.
	gap := ReadAdaptiveGap()
	readyAt := stopTime.Add(gap)
	if readyAt.Before(time.Now()) {
		return time.Now()
	}
	return readyAt
}

// settleDelay — базовая пауза ПОСЛЕ того как netsh сообщил "интерфейс свободен".
//
// ОПТИМИЗАЦИЯ: снижено с 15с до 5с — RemoveStaleTunAdapter теперь ждёт реального
// завершения pnputil /remove-device синхронно, поэтому к моменту settle PnP уже чист.
// 5с остаётся как страховка: SWD bus driver иногда ещё 1-3с финализирует registry
// entries после того как Get-PnpDevice уже говорит absent.
// settleDelay адаптивен: растёт пропорционально gap (25% от gap, min 5с, max 20с).
const settleDelayBase = 5 * time.Second
const settleDelayMax  = 20 * time.Second

// readSettleDelay вычисляет актуальный settle delay на основе текущего адаптивного gap.
// settle = max(settleDelayBase, gap * 0.25), не более settleDelayMax.
func ReadSettleDelay() time.Duration {
	gap := ReadAdaptiveGap()
	d := time.Duration(float64(gap) * 0.25)
	if d < settleDelayBase {
		d = settleDelayBase
	}
	if d > settleDelayMax {
		d = settleDelayMax
	}
	return d
}

// sleepCtx спит duration или возвращает false если ctx отменён раньше.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

// PollUntilFree ждёт пока wintun kernel-объект реально освободится.
//
// v5: четыре пути в зависимости от состояния предыдущей очистки:
//
//  0. CleanShutdownFile присутствует → sing-box остановлен штатно через Stop().
//     WintunCloseAdapter уже вызван, PnP-запись в порядке.
//     Пропускаем ВСЁ (gap + confirm-loop + settle) — идём прямо к запуску.
//     Типичное ожидание: ~0 мс. Аналог WireGuard для Windows: fixed GUID + reuse.
//
//  1. FastDeleteFile присутствует → ForceDeleteAdapter вернул true, kernel-объект
//     уже освобождён синхронно через DLL. Пропускаем adaptive gap полностью,
//     переходим сразу к confirm-loop (3× probe) + settle-delay.
//     Типичное ожидание: ~7–12 с вместо ~65 с.
//
//  2. Холодный старт (StopFile старше coldStartThreshold) → Windows GC давно отработал,
//     gap пропускаем, но confirm-loop выполняем — RemoveStaleTunAdapter удаляет адаптер
//     асинхронно и нужно убедиться что интерфейс исчез. + settle-delay.
//
//  3. Горячий старт без fast-delete → ждём оставшееся время adaptive gap, затем
//     confirm-loop + settle-delay.
//
// Settle-delay (ReadSettleDelay): дополнительная пауза ПОСЛЕ confirm-loop.
// Закрывает race-window между "probe=free" и реальным CreateAdapter в sing-box.
// Без этого WARN[0010]+FATAL[0015] повторяются даже при корректном probe.
//
// ctx позволяет прервать ожидание при выходе из приложения.
func PollUntilFree(ctx context.Context, log logger.Logger, ifName string) {
	// Нет StopFile → первый запуск ever, ничего ждать не нужно.
	if _, err := os.Stat(StopFile); os.IsNotExist(err) {
		return
	}

	// ── Путь 0: чистое завершение — самый быстрый старт ─────────────────────
	// ОПТИМИЗАЦИЯ: аналог WireGuard для Windows (fixed GUID + WintunCloseAdapter).
	// При штатной остановке sing-box через Stop() (CTRL_BREAK → Wait) xray.Manager
	// вызывает RecordCleanShutdown(). Это гарантирует что WintunCloseAdapter выполнен,
	// kernel-объект корректно освобождён, PnP-запись в порядке.
	// Следующий WintunCreateAdapter не найдёт конфликтов → никакого ожидания.
	if _, err := os.Stat(CleanShutdownFile); err == nil {
		_ = os.Remove(CleanShutdownFile) // одноразовый сигнал
		log.Info("wintun: чистое завершение — пропускаем все задержки (gap=0, settle=0)")
		return
	}

	// ── Определяем путь ожидания ─────────────────────────────────────────────

	// Путь 1: fast-delete — ForceDeleteAdapter уже освободил kernel-объект синхронно.
	// Удаляем маркер сразу (одноразовый сигнал) и пропускаем gap.
	fastDeleted := false
	if _, err := os.Stat(FastDeleteFile); err == nil {
		_ = os.Remove(FastDeleteFile)
		fastDeleted = true
		log.Info("wintun: fast-delete detected — kernel-объект освобождён через DLL, gap пропущен")
	}

	// Путь 2: холодный старт — Windows GC давно завершён, gap не нужен.
	// НО confirm-loop всё равно выполняем — RemoveStaleTunAdapter удаляет адаптер
	// асинхронно и даже при холодном старте нужно убедиться что интерфейс реально
	// исчез до запуска sing-box. Без этого: холодный старт → gap=0 → sing-box
	// стартует до того как Windows применил Remove-NetAdapter → WARN[0010] + FATAL[0015].
	coldStart := false
	if !fastDeleted {
		if data, err := os.ReadFile(StopFile); err == nil {
			if ns, err2 := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64); err2 == nil {
				if elapsed := time.Since(time.Unix(0, ns)); elapsed > coldStartThreshold {
					log.Info("wintun: холодный старт (%v с остановки) — gap пропущен, проверяем адаптер...",
						elapsed.Round(time.Second))
					coldStart = true
				}
			}
		}
	}

	// ── Путь 3: горячий старт без fast-delete — adaptive gap ─────────────────
	if !fastDeleted && !coldStart {
		gap := ReadAdaptiveGap()
		// Учитываем уже прошедшее время с момента остановки (StopFile timestamp).
		alreadyWaited := time.Duration(0)
		if data, err := os.ReadFile(StopFile); err == nil {
			if ns, err2 := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64); err2 == nil {
				alreadyWaited = time.Since(time.Unix(0, ns))
			}
		}
		remaining := gap - alreadyWaited
		if remaining < 0 {
			remaining = 0
		}

		if remaining > 0 {
			log.Info("wintun: ждём освобождения kernel-объекта (gap=%v, уже прошло=%v, осталось=%v)...",
				gap.Round(time.Second), alreadyWaited.Round(time.Second), remaining.Round(time.Second))
			if !sleepCtx(ctx, remaining) {
				log.Info("wintun: ожидание прервано (выход из приложения)")
				return
			}
		}
	}

	// ── Confirm-loop: стабильное "free" состояние ─────────────────────────────
	//
	// Требуем confirmRequired последовательных "free" проверок с интервалом 500мс.
	// После fast-delete или холодного старта объект почти наверняка уже свободен,
	// поэтому уменьшили с 6 до 3 (1.5 с стабильности вместо 3 с).
	// Race-window после gap закрывается settle-delay, а не длиной confirm-loop.
	const (
		confirmInterval = 500 * time.Millisecond
		confirmRequired = 3  // 3 × 500мс = 1.5с стабильного "free" (было 6)
		confirmTimeout  = 60 * time.Second
	)
	confirmCount := 0
	pnpRetry := false // флаг: уже делали pnputil sync removal → используем короткий settle
	confirmDeadline := time.Now().Add(confirmTimeout)
	log.Info("wintun: gap истёк — ждём стабильного освобождения (%d× probe)...", confirmRequired)
	for time.Now().Before(confirmDeadline) {
		probeOK := kernelObjectFree(ifName)
		netshOK := !InterfaceExists(ifName)
		if probeOK && netshOK {
			confirmCount++
			if confirmCount >= confirmRequired {
				// ── PnP проверка ДО settle-delay ──────────────────────────
				// ОПТИМИЗАЦИЯ: проверяем PnP-уровень СРАЗУ после confirm-loop,
				// а не после 15с settle-delay. Это экономит ~15с в случае
				// "removal pending" — найденном в логе 20:19:17→20:19:32.
				// kernel+netsh свободны (probeOK+netshOK), но PnP база ещё не обновлена.
				// Проверяем сразу → если pending → pnputil → короткий settle.
				if NetAdapterExists(ifName) {
					log.Warn("wintun: PnP устройство %s в состоянии removal pending — pnputil sync removal...", ifName)
					psForce := `$ids = (Get-PnpDevice | Where-Object { $_.FriendlyName -like '*` + ifName + `*' -or $_.FriendlyName -like '*wintun*' -or $_.FriendlyName -like '*WireGuard*' -or $_.FriendlyName -like '*sing-tun*' }).InstanceId; if ($ids) { $ids | ForEach-Object { pnputil /remove-device $_ 2>&1 | Out-Null } }`
					cmd := exec.Command("powershell", "-WindowStyle", "Hidden", "-NonInteractive", "-Command", psForce)
					cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
					_, _ = cmd.CombinedOutput()
					pnpRetry = true
					// Поллим пока Get-PnpDevice не вернёт absent (до 10с).
					for i := 0; i < 20; i++ {
						if !sleepCtx(ctx, 500*time.Millisecond) {
							return
						}
						if !NetAdapterExists(ifName) {
							log.Info("wintun: PnP устройство удалено через pnputil (%d× 500мс)", i+1)
							break
						}
					}
					// Сбрасываем счётчик — нужен ещё один раунд подтверждений
					confirmCount = 0
					continue
				}

				// ── Settle-delay ───────────────────────────────────────────
				// PnP чист (pnp=absent) — ждём settle перед запуском sing-box.
				// Закрывает race-window между "probe=free" и CreateAdapter:
				// kernel+netsh+pnp свободны, но Windows может ещё не завершить
				// все внутренние операции. После pnputil retry — короткий settle (3с).
				settle := ReadSettleDelay()
				if pnpRetry {
					settle = 3 * time.Second
				}
				log.Info("wintun: %d× probe=free — settle-delay %v перед запуском...",
					confirmRequired, settle.Round(time.Second))
				if !sleepCtx(ctx, settle) {
					log.Info("wintun: settle прерван (выход из приложения)")
					return
				}

				log.Info("wintun: готов к запуску sing-box (%d× probe=free, netsh=absent, pnp=absent)", confirmRequired)
				return
			}
		} else {
			// Сброс счётчика при любом "занято"
			if confirmCount > 0 {
				log.Info("wintun: probe занято после %d подтверждений — сброс счётчика", confirmCount)
			}
			confirmCount = 0
		}
		if !sleepCtx(ctx, confirmInterval) {
			log.Info("wintun: ожидание прервано (выход из приложения)")
			return
		}
	}
	log.Warn("wintun: confirm timeout %v — запускаем несмотря на занятость", confirmTimeout)
}

// InterfaceExists проверяет через netsh присутствует ли интерфейс в TCP/IP стеке.
func InterfaceExists(ifName string) bool {
	cmd := exec.Command("netsh", "interface", "show", "interface", ifName)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false // "The specified interface does not exist" → свободен
	}
	return strings.Contains(string(out), ifName)
}

// NetAdapterExists проверяет существование адаптера на уровне PnP / Device Manager
// через Get-PnpDevice — надёжнее чем Get-NetAdapter и netsh для обнаружения
// устройств в любом состоянии, включая "removal pending".
//
// BUG FIX: Get-NetAdapter и InterfaceExists (netsh) видят устройство как ОТСУТСТВУЮЩЕЕ
// сразу после Remove-PnpDevice, хотя PnP manager ещё не завершил удаление из своей базы.
// В этом состоянии WintunCreateAdapter("tun0") получает ответ PnP "removal pending, wait"
// и делает 5 попыток × 3с = 15с ожидания → WARN[0010] → FATAL[0015].
//
// Get-PnpDevice возвращает устройства в ЛЮБОМ состоянии, включая "removal pending".
// Это позволяет confirm-loop корректно ждать пока PnP НА САМОМ ДЕЛЕ завершит удаление.
func NetAdapterExists(ifName string) bool {
	// Ищем любое wintun/tun0/sing-tun устройство через PnP (а не Get-NetAdapter).
	// sing-tun: TunnelType="sing-tun" → FriendlyName="sing-tun Tunnel" в Device Manager.
	// Возвращает True пока устройство присутствует в PnP в ЛЮБОМ состоянии.
	cmd := exec.Command("powershell",
		"-WindowStyle", "Hidden", "-NonInteractive", "-Command",
		`(Get-PnpDevice | Where-Object { $_.FriendlyName -like '*`+ifName+`*' -or $_.FriendlyName -like '*wintun*' -or $_.FriendlyName -like '*WireGuard*' -or $_.FriendlyName -like '*sing-tun*' } | Measure-Object).Count -gt 0`)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "True"
}

// repairStaleDriver удаляет устаревшую запись wintun из SCM если она сломана.
// После sc delete wintun — sing-box переустановит драйвер автоматически при следующем запуске.
// Вызывается при каждом RemoveStaleTunAdapter: операция идемпотентна (no-op если драйвер в норме).
func repairStaleDriver(log logger.Logger) {
	cmd := exec.Command("sc", "query", "wintun")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
	out, _ := cmd.CombinedOutput()
	outStr := string(out)
	// Признаки stale registration: запись есть но состояние не RUNNING и не STOP_PENDING.
	// Если драйвера нет вообще — sc query вернёт error, ничего не делаем.
	if strings.Contains(outStr, "STATE") &&
		!strings.Contains(outStr, "RUNNING") &&
		!strings.Contains(outStr, "STOP_PENDING") &&
		!strings.Contains(outStr, "STOPPED") {
		del := exec.Command("sc", "delete", "wintun")
		del.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
		if delOut, delErr := del.CombinedOutput(); delErr == nil {
			log.Info("wintun: stale driver registration удалена — будет переустановлена при старте (%s)", strings.TrimSpace(string(delOut)))
		} else {
			log.Info("wintun: не удалось удалить stale driver: %s", cleanSCOutput(string(delOut)))
		}
	}

	// FIX: дополнительно проверяем через sc query win32_own_service —
	// если EXIT_CODE содержит 433 (ERROR_DEV_NOT_EXIST) или 31 (ERROR_GEN_FAILURE),
	// драйвер скорее всего в состоянии ошибки и sc delete нужен даже при STOPPED.
	// BUG FIX: sc.exe требует "key= value" как один токен без пробела между "=" и значением.
	// Передача "type=", "driver" как отдельных argv[] разрывала пару — sc игнорировал фильтр.
	cmd2 := exec.Command("sc", "query", "type= driver", "state= all")
	cmd2.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
	out2, _ := cmd2.CombinedOutput()
	// После исправления синтаксиса sc query список включает ВСЕХ драйверов.
	// Делим вывод на блоки по "SERVICE_NAME:" и ищем только блок wintun.
	outStr2 := strings.ToUpper(string(out2))
	wintunBlock := ""
	for _, block := range strings.Split(outStr2, "SERVICE_NAME:") {
		if strings.Contains(block, "WINTUN") {
			wintunBlock = block
			break
		}
	}
	if (strings.Contains(wintunBlock, "WIN32_EXIT_CODE") &&
		(strings.Contains(wintunBlock, " 433") || strings.Contains(wintunBlock, " 31"))) ||
		strings.Contains(wintunBlock, "FAILED_PERMANENT") {
		del2 := exec.Command("sc", "delete", "wintun")
		del2.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
		if delOut2, delErr2 := del2.CombinedOutput(); delErr2 == nil {
			log.Info("wintun: driver с exit code 433/31 удалён — будет переустановлен (%s)", strings.TrimSpace(string(delOut2)))
		}
	}
}

// RemoveStaleTunAdapter выполняет очистку TUN-адаптера:
//   - sc stop wintun (для случая когда драйвер всё же в SCM)
//   - Remove-NetAdapter (убирает из Device Manager)
//
// OPT: taskkill и sc stop выполняются параллельно — они не зависят друг от друга.
// Это сокращает последовательное ожидание с ~5с до ~3с.
//
// После ForceDeleteAdapter записываем FastDeleteFile-маркер если DLL-удаление прошло
// успешно. PollUntilFree читает его и пропускает длинный gap (60–180с), переходя
// сразу к confirm-loop + settle-delay (~10–15с итого вместо ~65с).
func RemoveStaleTunAdapter(log logger.Logger) {
	// Проверяем stale driver registration (ERROR_GEN_FAILURE = error 31).
	// Симптом: "A device attached to the system is not functioning."
	// Причина: запись wintun в SCM устарела после Windows update — драйвер .sys в порядке,
	// но регистрация сломана. Решение: sc delete wintun → wintun переустановит себя сам
	// при следующем CreateAdapter (по аналогии с netbird issue #5408).
	repairStaleDriver(log)
	runHidden := func(name string, args ...string) (string, error) {
		cmd := exec.Command(name, args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{
			CreationFlags: 0x08000000,
			HideWindow:    true,
		}
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}

	// ── Фаза 1: параллельно убиваем sing-box и останавливаем wintun SCM-драйвер ──
	// Эти два шага независимы друг от друга — выполняем одновременно.
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		// Убиваем любой оставшийся процесс sing-box.exe перед очисткой TUN.
		// Это критично: если sing-box ещё жив (даже в состоянии завершения), он держит
		// WinTun kernel-объект открытым → новый sing-box получает FATAL[0015] через 15с.
		taskkillOut, _ := runHidden("taskkill", "/F", "/IM", "sing-box.exe", "/T")
		// BUG FIX: убрано второе условие `!strings.Contains(..., "not found")`.
		// Оно давало true для ЛЮБОГО вывода без "not found" — включая "Access is denied",
		// "Invalid parameter" и т.д. Код ложно логировал "завершён" и входил в wait-loop.
		if strings.Contains(taskkillOut, "SUCCESS") {
			log.Info("wintun: sing-box.exe завершён принудительно, ждём выгрузки...")
			// Ждём пока процесс действительно исчезнет из системы (до 3с)
			for i := 0; i < 6; i++ {
				time.Sleep(500 * time.Millisecond)
				chkOut, _ := runHidden("tasklist", "/FI", "IMAGENAME eq sing-box.exe", "/NH")
				if !strings.Contains(strings.ToLower(chkOut), "sing-box.exe") {
					log.Info("wintun: sing-box.exe выгружен из памяти")
					break
				}
			}
		}
	}()

	go func() {
		defer wg.Done()
		// Попытка остановить wintun через SCM (работает если драйвер зарегистрирован).
		// Параллельно с taskkill — не зависим от него.
		out, err := runHidden("sc", "stop", "wintun")
		if err != nil {
			log.Info("wintun driver не запущен (%s) — первый запуск или уже остановлен",
				cleanSCOutput(out))
		} else {
			log.Info("wintun driver: отправлена команда остановки")
			// Ждём STOP_PENDING → STOPPED (до 5 секунд)
			for i := 0; i < 10; i++ {
				time.Sleep(500 * time.Millisecond)
				status, _ := runHidden("sc", "query", "wintun")
				if strings.Contains(status, "STOPPED") {
					log.Info("wintun driver остановлен (STOPPED)")
					break
				}
			}
		}
	}()

	wg.Wait()
	// Дополнительная пауза для освобождения kernel handles после завершения обоих процессов
	time.Sleep(300 * time.Millisecond)

	// ── Фаза 2: DLL-удаление kernel-объекта ──────────────────────────────────
	// Шаг 0: WintunDeleteAdapter — удаляем kernel-объект напрямую через DLL.
	// Это то что не делают ни Remove-NetAdapter, ни netsh, ни taskkill.
	// WintunDeleteAdapter освобождает \\Device\\WINTUN-{GUID} синхронно, без ожидания Windows GC.
	// WARN[0010]+FATAL[0015] возникают именно потому что этот объект остаётся занятым.
	//
	// Если ForceDeleteAdapter вернул true — kernel-объект освобождён синхронно.
	// Записываем маркер FastDeleteFile: PollUntilFree увидит его и пропустит длинный
	// adaptive gap, перейдя сразу к confirm-loop + settle-delay.
	deleted := ForceDeleteAdapter(tunInterfaceName) // BUG FIX #5: используем константу вместо "tun0"
	if deleted {
		log.Info("wintun: ForceDeleteAdapter succeeded — kernel-объект освобождён синхронно, gap будет пропущен")
		_ = os.WriteFile(FastDeleteFile, []byte("1"), 0644)
	} else {
		log.Info("wintun: ForceDeleteAdapter не сработал — PollUntilFree применит полный adaptive gap")
		_ = os.Remove(FastDeleteFile) // Убираем устаревший маркер если есть
	}
	time.Sleep(200 * time.Millisecond) // даём DLL завершить освобождение

	// ── Фаза 3: очистка сетевого стека, Device Manager и SWD реестра ─────────

	// Шаг 1: TCP/IP стек через netsh.
	_, _ = runHidden("netsh", "interface", "ip", "delete", "interface", tunInterfaceName)
	_, _ = runHidden("netsh", "interface", "ipv6", "delete", "interface", tunInterfaceName)

	// Шаг 2: Device Manager — синхронное удаление через pnputil.
	//
	// КЛЮЧЕВОЕ ИСПРАВЛЕНИЕ ПОРЯДКА:
	// Предыдущий код делал Remove-PnpDevice (async) → psGetIds → pnputil.
	// Проблема: после Remove-PnpDevice устройство мгновенно исчезает из Get-PnpDevice
	// (логически удалено), поэтому psGetIds возвращал пустой список и pnputil
	// никогда не вызывался. Физическое удаление оставалось pending.
	//
	// Правильный порядок:
	//   1. Получить InstanceId СНАЧАЛА (пока устройство видно Get-PnpDevice)
	//   2. pnputil /remove-device — синхронная операция (блокирует до завершения PnP)
	//   3. Проверить что устройство исчезло из Get-PnpDevice
	//
	// Remove-NetAdapter убирает из TCP/IP стека (не нужно отдельного netsh для этого).
	psRemove := `Remove-NetAdapter -Name '` + tunInterfaceName + `' -Confirm:$false -ErrorAction SilentlyContinue`
	_, _ = runHidden("powershell", "-WindowStyle", "Hidden", "-NonInteractive", "-Command", psRemove)

	// Шаг 2a: Получаем InstanceId-ы ПЕРЕД удалением — пока устройства ещё видны.
	// BUG FIX: sing-box = "sing-tun Tunnel", не "wintun"/"WireGuard".
	pnpFilter := `$_.FriendlyName -like '*WireGuard*' -or $_.FriendlyName -like '*wintun*' -or $_.FriendlyName -like '*tun0*' -or $_.FriendlyName -like '*sing-tun*'`
	psGetIds := `(Get-PnpDevice | Where-Object { ` + pnpFilter + ` }).InstanceId -join "|"`
	idsOut, _ := runHidden("powershell", "-WindowStyle", "Hidden", "-NonInteractive", "-Command", psGetIds)

	if idsOut != "" {
		// Шаг 2b: pnputil /remove-device — синхронный, блокирует до реального завершения PnP.
		// В отличие от Remove-PnpDevice (async), pnputil ждёт пока PnP database обновится.
		for _, instanceID := range strings.Split(idsOut, "|") {
			instanceID = strings.TrimSpace(instanceID)
			if instanceID == "" {
				continue
			}
			out, err := runHidden("pnputil", "/remove-device", instanceID)
			if err == nil {
				log.Info("wintun: pnputil синхронно удалил PnP устройство: %s", instanceID)
			} else {
				// pnputil вернул ошибку — fallback на async Remove-PnpDevice
				log.Info("wintun: pnputil /remove-device: %s (%v) — fallback на Remove-PnpDevice", strings.TrimSpace(out), err)
				psRm := `Remove-PnpDevice -InstanceId '` + instanceID + `' -Confirm:$false -ErrorAction SilentlyContinue`
				_, _ = runHidden("powershell", "-WindowStyle", "Hidden", "-NonInteractive", "-Command", psRm)
			}
		}

		// Шаг 2c: Поллим Get-PnpDevice до реального исчезновения (до 5с).
		// Подтверждает что PnP database обновлена — PollUntilFree не увидит removal pending.
		for i := 0; i < 10; i++ {
			time.Sleep(500 * time.Millisecond)
			checkOut, _ := runHidden("powershell", "-WindowStyle", "Hidden", "-NonInteractive", "-Command",
				`(Get-PnpDevice | Where-Object { `+pnpFilter+` } | Measure-Object).Count -gt 0`)
			if strings.TrimSpace(checkOut) != "True" {
				log.Info("wintun: PnP устройства удалены из Device Manager (%d× 500мс)", i+1)
				break
			}
		}
	} else {
		log.Info("wintun: PnP устройств для удаления не найдено")
	}

	// Шаг 3: КЛЮЧЕВОЙ ФИКС — прямая очистка SWD (Software Device) реестра.
	//
	// Исследование Tailscale/sing-box/wireguard-windows issues подтвердило:
	//   - FATAL[0015] "Cannot create a file when that file already exists" происходит
	//     внутри wintun.dll при SetupDiCallClassInstaller(DIF_INSTALLDEVICE) когда
	//     HKLM\...\Enum\SWD\Wintun\ содержит запись от предыдущей сессии.
	//   - Get-PnpDevice / kernelObjectFree / netsh — все могут вернуть "absent" РАНЬШЕ
	//     чем SWD bus driver очищает свои registry entries.
	//   - Procmon trace (sing-box issue #3725) наглядно показывает: wintun обращается
	//     к HKLM\System\CurrentControlSet\Enum\SWD\Wintun\{NetCfgInstanceId}. При наличии
	//     stale ключа → ERROR_ALREADY_EXISTS (0xB7) → FATAL[0015].
	//   - Аналогичный подход используется в Cloudflare WARP fix:
	//     `pnputil /delete-driver oem##.inf` + `pnputil /scan-devices`.
	//
	// reg delete SWD\Wintun — убирает все stale registry entries напрямую.
	// После этого wintun при следующем CreateAdapter создаёт запись с нуля.
	swdKey := `HKLM\SYSTEM\CurrentControlSet\Enum\SWD\Wintun`
	if out, err := runHidden("reg", "delete", swdKey, "/f"); err == nil {
		log.Info("wintun: SWD\\Wintun реестровые записи удалены (%s)", strings.TrimSpace(out))
	} else {
		// Ключ не существует — нормально для первого запуска
		log.Info("wintun: SWD\\Wintun ключ уже чист или не найден")
	}

	// Шаг 4 (финал): короткая пауза — даём Windows время применить все изменения.
	// pnputil уже отработал синхронно, 300мс достаточно для финального оседания.
	time.Sleep(300 * time.Millisecond)

	log.Info("TUN cleanup завершён")
}

// Shutdown отправляет команду остановки без ожидания (используется при выходе из приложения).
func Shutdown(log logger.Logger) {
	run := func(name string, args ...string) {
		cmd := exec.Command(name, args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
		_ = cmd.Run()
	}
	run("sc", "stop", "wintun")
	// Убираем интерфейс из TCP/IP стека при выходе — ускоряет следующий запуск
	run("netsh", "interface", "ip", "delete", "interface", tunInterfaceName)
	run("netsh", "interface", "ipv6", "delete", "interface", tunInterfaceName)
	run("powershell", "-WindowStyle", "Hidden", "-NonInteractive", "-Command",
		"Remove-NetAdapter -Name '"+tunInterfaceName+"' -Confirm:$false -ErrorAction SilentlyContinue")
	log.Info("TUN shutdown отправлен")
}
