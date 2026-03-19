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
		// Уже валидный UTF-8 — убираем лишние пробелы и возвраты каретки
		return strings.TrimSpace(strings.ReplaceAll(raw, "\r", ""))
	}
	// Невалидный UTF-8 (CP1251 на русских Windows) — извлекаем только код ошибки
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

// minGap — минимальное время между остановкой sing-box и следующим стартом.
// Даже если netsh говорит что интерфейс свободен, wintun kernel-объект
// \Device\WINTUN-{GUID} может ещё жить. minGap гарантирует Windows GC.
const minGap = 35 * time.Second

// PollUntilFree обеспечивает два условия перед запуском sing-box:
//  1. Минимальный gap 60с от последней остановки (timestamp из StopFile).
//     Даже если netsh говорит "интерфейс не найден", kernel-объект может жить.
//     Это исправляет краш FATAL[0015] при быстром перезапуске.
//  2. Активный polling через netsh — ждём пока tun0 исчезнет из TCP/IP стека.
//
// Почему netsh, а не Get-NetAdapter:
//
//	Get-NetAdapter смотрит в PnP/Device Manager — после Remove-NetAdapter
//	адаптер там исчезает немедленно, но kernel-объект ещё жив.
//	netsh держит ссылку дольше и отпускает её синхронно с kernel GC.
func PollUntilFree(log logger.Logger, ifName string) {
	// Шаг 1: минимальный gap от последней остановки.
	// Даже если netsh говорит "свободен" — ждём чтобы Windows GC успел
	// освободить kernel-объект \Device\WINTUN-{GUID}.
	if data, err := os.ReadFile(StopFile); err == nil {
		if ns, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64); err == nil {
			elapsed := time.Since(time.Unix(0, ns))
			if wait := minGap - elapsed; wait > 0 {
				log.Info("wintun: прошло %v с последней остановки — ждём ещё %v (min gap %v)...",
					elapsed.Round(time.Second), wait.Round(time.Second), minGap)
				time.Sleep(wait)
				log.Info("wintun: min gap выдержан — проверяем TCP/IP стек")
			}
		}
	}

	// Шаг 2: polling по TCP/IP стеку.
	if !InterfaceExists(ifName) {
		log.Info("wintun: интерфейс %s не найден в TCP/IP стеке — готов", ifName)
		return
	}

	log.Info("wintun: ждём освобождения интерфейса %s (polling каждые 500мс, max %v)...",
		ifName, PollTimeout)
	deadline := time.Now().Add(PollTimeout)
	for time.Now().Before(deadline) {
		time.Sleep(PollInterval)
		if !InterfaceExists(ifName) {
			log.Info("wintun: интерфейс %s освобождён — продолжаем", ifName)
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
