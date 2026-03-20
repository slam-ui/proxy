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
func EstimateReadyAt() time.Time {
	data, err := os.ReadFile(StopFile)
	if err != nil {
		return time.Now() // нет файла — gap не нужен
	}
	ns, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return time.Now()
	}
	stopTime := time.Unix(0, ns)
	elapsed := time.Since(stopTime)

	if elapsed > coldStartThreshold {
		return time.Now() // холодный старт — сразу готов
	}
	minGap := ReadAdaptiveGap()
	// +settle delay учитываем в ETA
	readyAt := stopTime.Add(minGap + readSettleDelay())
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
func readSettleDelay() time.Duration {
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
func PollUntilFree(log logger.Logger, ifName string) {
	// Шаг 1: адаптивный gap от последней остановки.
	if data, err := os.ReadFile(StopFile); err == nil {
		if ns, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64); err == nil {
			elapsed := time.Since(time.Unix(0, ns))

			if elapsed > coldStartThreshold {
				// Холодный старт: прошло >5 мин — Windows GC уже освободил всё.
				// Пропускаем gap и сразу переходим к polling (шаг 2).
				log.Info("wintun: прошло %v с остановки — холодный старт, gap пропущен",
					elapsed.Round(time.Second))
			} else {
				// Горячий рестарт: применяем адаптивный gap.
				minGap := ReadAdaptiveGap()
				if wait := minGap - elapsed; wait > 0 {
					log.Info("wintun: прошло %v с последней остановки — ждём ещё %v (min gap %v)...",
						elapsed.Round(time.Second), wait.Round(time.Second), minGap)
					time.Sleep(wait)
					log.Info("wintun: min gap выдержан — проверяем TCP/IP стек")
				}
			}
		}
	}
	// Нет StopFile → первый запуск вообще, gap не нужен.

	// Шаг 2: polling по TCP/IP стеку.
	if !InterfaceExists(ifName) {
		// Интерфейса нет в netsh — но kernel может ещё не освободить объект.
		// Шаг 3: settle delay перекрывает разрыв между "netsh свободен" и "kernel free".
		sd := readSettleDelay()
		log.Info("wintun: интерфейс %s не найден в TCP/IP стеке — settle %v...", ifName, sd)
		time.Sleep(sd)
		log.Info("wintun: settle завершён — готов")
		return
	}

	log.Info("wintun: ждём освобождения интерфейса %s (polling каждые 500мс, max %v)...",
		ifName, PollTimeout)
	deadline := time.Now().Add(PollTimeout)
	for time.Now().Before(deadline) {
		time.Sleep(PollInterval)
		if !InterfaceExists(ifName) {
			// Нашли момент исчезновения — ждём settle перед стартом.
			sd := readSettleDelay()
			log.Info("wintun: интерфейс %s освобождён — settle %v...", ifName, sd)
			time.Sleep(sd)
			log.Info("wintun: settle завершён — готов")
			return
		}
	}
	log.Warn("wintun: timeout %v — интерфейс %s всё ещё занят, запускаем sing-box всё равно",
		PollTimeout, ifName)
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

// RemoveStaleTunAdapter выполняет очистку TUN-адаптера:
//   - sc stop wintun (для случая когда драйвер всё же в SCM)
//   - Remove-NetAdapter (убирает из Device Manager)
func RemoveStaleTunAdapter(log logger.Logger) {
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
