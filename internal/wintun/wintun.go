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
}

// ── Адаптивный gap ────────────────────────────────────────────────────────────

// gapFile хранит текущий адаптивный minGap (в наносекундах) между сессиями.
// Значение увеличивается при каждом wintun-краше и сбрасывается при чистом выходе.
const gapFile = "wintun_gap_ns"

// minGapBase — базовый (минимальный) gap без крашей.
// 60с вместо старых 35с: логи показывают что FATAL[0015] стабильно происходит
// при gap < 60с — wintun kernel-объект на этой машине живёт дольше 35с.
const minGapBase = 60 * time.Second

// minGapMax — потолок адаптивного gap.
// 3 минуты: gap сбрасывается после каждого успешного старта (ResetAdaptiveGap),
// поэтому он растёт только в рамках одной неудачной сессии.
const minGapMax = 3 * time.Minute

// coldStartThreshold — если StopFile старше этого времени, считаем холодным
// стартом: Windows GC давно отработал, gap не нужен совсем.
const coldStartThreshold = 5 * time.Minute

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
func EstimateReadyAt() time.Time {
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
// Логи показывают что 5с недостаточно: два краша подряд с settle 5с.
// settleDelay адаптивен: растёт пропорционально gap (25% от gap, min 15с, max 45с).
// Это отражает реальность — чем больше gap нужен, тем медленнее Windows GC на данной машине.
const settleDelayBase = 15 * time.Second
const settleDelayMax  = 45 * time.Second

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

// PollUntilFree обеспечивает три условия перед запуском sing-box:
//  1. Адаптивный gap от последней остановки (60с..3мин).
//     Быстрый путь: если StopFile старше coldStartThreshold (5 мин) —
//     пропускаем ожидание (холодный старт, Windows GC давно завершил работу).
//     При каждом wintun-краше gap удваивается через IncreaseAdaptiveGap.
//     При чистом выходе — сбрасывается через ResetAdaptiveGap.
//  2. Активный polling через netsh — ждём пока tun0 исчезнет из TCP/IP стека.
//  3. settle delay — дополнительная пауза после исчезновения из netsh,
//     чтобы kernel успел освободить \Device\WINTUN-{GUID}.
//
// Почему netsh, а не Get-NetAdapter:
//
//	Get-NetAdapter смотрит в PnP/Device Manager — после Remove-NetAdapter
//	адаптер там исчезает немедленно, но kernel-объект ещё жив.
//	netsh держит ссылку дольше и отпускает её синхронно с kernel GC.
// sleepCtx спит duration или возвращает false если ctx отменён раньше.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

// PollUntilFree обеспечивает три условия перед запуском sing-box:
//  1. Адаптивный gap от последней остановки.
//  2. Активный polling через netsh — ждём пока tun0 исчезнет из TCP/IP стека.
//  3. settle delay — дополнительная пауза после исчезновения из netsh.
//
// ctx позволяет прервать ожидание при выходе из приложения.
// Если ctx отменён — функция возвращается немедленно без паники.
// PollUntilFree ждёт пока wintun kernel-объект реально освободится.
//
// v3: прямое зондирование через wintun.dll вместо временного gap.
//
// Старый подход (gap): ждать фиксированное время (60-240с) в надежде что GC завершился.
// Проблема: gap слишком большой для большинства случаев (GC занимает 5-30с),
//            и слишком маленький в редких случаях (отсюда крашей).
//
// Новый подход (probe): вызываем WintunOpenAdapter каждые 500мс.
//   - Вернул NULL + любая ошибка → kernel объект свободен → старт.
//   - Вернул handle → объект ещё жив, закрываем handle, ждём.
// Это точный сигнал без угадывания.
//
// Минимальный gap (5с) оставлен как защита от race: между "объект свободен"
// и реальным CreateAdapter в sing-box должно пройти хотя бы несколько секунд.
func PollUntilFree(ctx context.Context, log logger.Logger, ifName string) {
	// Холодный старт: StopFile старше coldStartThreshold → GC давно завершён.
	if data, err := os.ReadFile(StopFile); err == nil {
		if ns, err2 := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64); err2 == nil {
			if elapsed := time.Since(time.Unix(0, ns)); elapsed > coldStartThreshold {
				log.Info("wintun: холодный старт (%v с остановки) — ожидание пропущено",
					elapsed.Round(time.Second))
				return
			}
		}
	}
	// Нет StopFile → первый запуск, ничего ждать не нужно.
	if _, err := os.Stat(StopFile); os.IsNotExist(err) {
		return
	}

	// ── Надёжный алгоритм ожидания ──────────────────────────────────────────
	//
	// История проблемы:
	//   v1 (fixed gap): ждали фиксированное время → слишком долго для нормальных случаев
	//   v2 (adaptive gap + settle): ждали probe→"free" + settle → false-positive на этой машине
	//   v3 (double verify): probe + netsh двойная верификация → всё равно false-positive
	//
	// Ключевое наблюдение из логов: probe=free, netsh=absent, но FATAL[0015] всё равно.
	// WintunOpenAdapter и netsh не детектируют тот же объект что проверяет CreateAdapter.
	//
	// Надёжное решение:
	//   1. Ждём полный адаптивный gap ГАРАНТИРОВАННО (не сокращаем через probe).
	//      Gap уже учитывает историю крашей — при первом краше 60s, растёт до 5m.
	//   2. После gap — требуем стабильное "free" состояние 3 секунды подряд (6 проверок).
	//      Это защищает от единичного ложного "free" в момент проверки.
	//   3. Probe+netsh используем только чтобы ПРОДЛИТЬ ожидание, но не сократить.

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

	// После gap — ждём стабильного "free" состояния.
	// Требуем 6 последовательных "free" проверок с интервалом 500мс (= 3 секунды стабильности).
	// Это фильтрует единичные false-positive от WintunOpenAdapter.
	const (
		confirmInterval = 500 * time.Millisecond
		confirmRequired = 6  // 6 × 500мс = 3с стабильного "free"
		confirmTimeout  = 60 * time.Second
	)
	confirmCount := 0
	confirmDeadline := time.Now().Add(confirmTimeout)
	log.Info("wintun: gap истёк — ждём стабильного освобождения (%d× probe)...", confirmRequired)
	for time.Now().Before(confirmDeadline) {
		probeOK := kernelObjectFree(ifName)
		netshOK := !InterfaceExists(ifName)
		if probeOK && netshOK {
			confirmCount++
			if confirmCount >= confirmRequired {
				log.Info("wintun: готов к запуску sing-box (%d× probe=free, netsh=absent)", confirmRequired)
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
	cmd2 := exec.Command("sc", "query", "type=", "driver", "name=", "wintun")
	cmd2.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
	out2, _ := cmd2.CombinedOutput()
	outStr2 := strings.ToUpper(string(out2))
	if (strings.Contains(outStr2, "WIN32_EXIT_CODE") &&
		(strings.Contains(outStr2, " 433") || strings.Contains(outStr2, " 31"))) ||
		strings.Contains(outStr2, "FAILED_PERMANENT") {
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

	// Попытка остановить wintun через SCM (работает если драйвер зарегистрирован)
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

	// Удалить адаптер из Device Manager (косметика — убирает из списка сетей)
	// Удалить адаптер из Device Manager через Remove-NetAdapter
	psRemove := `Remove-NetAdapter -Name 'tun0' -Confirm:$false -ErrorAction SilentlyContinue`
	_, _ = runHidden("powershell", "-WindowStyle", "Hidden", "-NonInteractive", "-Command", psRemove)

	// FIX: "A device attached to the system is not functioning" (error 433 / ERROR_DEV_NOT_EXIST)
	// Причина: WintunOpenAdapter возвращает NULL (probe говорит "free"), но PnP-устройство
	// сломано — sing-box падает при CreateAdapter с ERROR_DEV_NOT_EXIST.
	// Решение: убрать сломанное PnP-устройство через Remove-PnpDevice перед стартом.
	// Remove-PnpDevice работает даже когда Remove-NetAdapter не видит адаптер.
	psPnp := `Get-PnpDevice | Where-Object { $_.FriendlyName -like '*WireGuard*' -or $_.FriendlyName -like '*wintun*' -or $_.FriendlyName -like '*tun0*' } | Where-Object { $_.Status -ne 'OK' -or $_.Problem -ne 0 } | ForEach-Object { Remove-PnpDevice -InstanceId $_.InstanceId -Confirm:$false -ErrorAction SilentlyContinue }`
	_, _ = runHidden("powershell", "-WindowStyle", "Hidden", "-NonInteractive", "-Command", psPnp)

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
	run("powershell", "-WindowStyle", "Hidden", "-NonInteractive", "-Command",
		"Remove-NetAdapter -Name 'tun0' -Confirm:$false -ErrorAction SilentlyContinue")
	log.Info("TUN shutdown отправлен")
}
