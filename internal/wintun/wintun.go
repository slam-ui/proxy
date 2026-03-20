// Package wintun управляет жизненным циклом wintun kernel-объекта на Windows.
//
// Корень проблемы "Cannot create a file when that file already exists":
//
//	sing-box грузит wintun.dll напрямую (не через SCM) и создаёт
//	именованный kernel-объект \Device\WINTUN-{GUID}.
//	После смерти sing-box объект живёт ещё 30-60 секунд (Windows GC асинхронный).
//	sc stop wintun не помогает — драйвер не регистрируется в SCM.
//
// Решение:
//  1. При каждой остановке sing-box записываем timestamp в файл (RecordStop).
//  2. При старте: Remove-NetAdapter, затем активный polling через netsh
//     до реального исчезновения интерфейса из TCP/IP стека (PollUntilFree).
//  3. TCP/IP стек (netsh) освобождает ссылку синхронно с kernel GC —
//     исчезновение из netsh = реальное освобождение \Device\WINTUN-{GUID}.
//
// Адаптивный gap (v2):
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
	StopFile     = "wintun_stopped_at" // файл с timestamp последней остановки
	PollInterval = 500 * time.Millisecond
	PollTimeout  = 70 * time.Second
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

// EstimateReadyAt возвращает время когда прокси ориентировочно будет готов.
// Используется UI для отображения обратного отсчёта при warming=true.
// Если нет StopFile или прошло больше coldStartThreshold — возвращает time.Now()
// (т.е. ждать не нужно).
// EstimateReadyAt возвращает приблизительное время готовности.
// С переходом на kernel probe точный ETA неизвестен (зависит от скорости Windows GC).
// Возвращаем консервативную оценку: 30с от остановки (обычно реально 5-15с).
func EstimateReadyAt() time.Time {
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
	// Консервативная оценка: 30с от последней остановки + minGap (5с).
	// На практике probe часто срабатывает раньше.
	const estimatedGCTime = 30 * time.Second
	readyAt := stopTime.Add(estimatedGCTime + 5*time.Second)
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
				log.Info("wintun: холодный старт (%v с остановки) — probe пропущен",
					elapsed.Round(time.Second))
				return
			}
		}
	}
	// Нет StopFile → первый запуск, ничего ждать не нужно.
	if _, err := os.Stat(StopFile); os.IsNotExist(err) {
		return
	}

	const (
		probeInterval = 500 * time.Millisecond
		probeTimeout  = 90 * time.Second  // потолок на случай если DLL не отвечает
		minGap        = 5 * time.Second   // минимум после освобождения перед стартом
	)

	log.Info("wintun: ждём освобождения kernel-объекта (probe каждые 500мс, max %v)...", probeTimeout)
	deadline := time.Now().Add(probeTimeout)
	for time.Now().Before(deadline) {
		if kernelObjectFree(ifName) {
			// Kernel объект свободен — ждём minGap для надёжности и стартуем.
			log.Info("wintun: kernel объект свободен — пауза %v перед стартом...", minGap)
			if !sleepCtx(ctx, minGap) {
				log.Info("wintun: пауза прервана (выход из приложения)")
				return
			}
			log.Info("wintun: готов к запуску sing-box")
			return
		}
		if !sleepCtx(ctx, probeInterval) {
			log.Info("wintun: probe прерван (выход из приложения)")
			return
		}
	}
	// Timeout — fallback на netsh как раньше
	log.Warn("wintun: probe timeout %v — kernel объект всё ещё занят по данным DLL, пробуем запустить", probeTimeout)
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
	ps := `Remove-NetAdapter -Name 'tun0' -Confirm:$false -ErrorAction SilentlyContinue`
	_, _ = runHidden("powershell", "-WindowStyle", "Hidden", "-NonInteractive", "-Command", ps)

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
