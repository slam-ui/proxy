// Package anomalylog отслеживает аномалии в event log и сохраняет
// их в файл с названием даты сессии для последующего анализа.
//
// Аномалии:
//   - Любое событие уровня error → немедленная запись в файл
//   - 3+ warning за 60 секунд → burst warning (всплеск ошибок)
//   - Краш sing-box (определяется по ключевым словам в сообщении)
//
// Формат файла: anomaly-2006-01-02_15-04-05.log
// Содержимое: контекст (последние 50 событий) + сами аномальные события.
package anomalylog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"proxyclient/internal/eventlog"
)

// AnomalyKind тип обнаруженной аномалии
type AnomalyKind string

const (
	KindError     AnomalyKind = "ERROR"      // событие уровня error
	KindWarnBurst AnomalyKind = "WARN_BURST" // всплеск предупреждений
	KindCrash     AnomalyKind = "CRASH"      // краш sing-box
)

// keywords краш-сигнатуры в сообщениях sing-box
var crashKeywords = []string{
	"fatal",
	"panic",
	"signal: killed",
	"access violation",
	"Cannot create a file when that file already exists",
	"configure tun interface",
	"bind: address already in use",
	"failed to start",
}

// Detector следит за event log и при обнаружении аномалии
// сохраняет диагностический файл.
type Detector struct {
	evLog        *eventlog.Log
	dir          string        // куда писать файлы (обычно папка с .exe)
	sessionTS    time.Time     // время запуска — используется в имени файла
	pollInterval time.Duration // интервал опроса (по умолчанию 3s)

	mu           sync.Mutex
	lastID       int             // ID последнего обработанного события
	warnTimes    []time.Time     // временные метки недавних warn (для burst-детекции)
	writtenFiles map[string]bool // уже созданные файлы (дедупликация)

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// New создаёт детектор.
// dir — директория для файлов аномалий (обычно filepath.Dir(os.Executable())).
func New(evLog *eventlog.Log, dir string) *Detector {
	return &Detector{
		evLog:        evLog,
		dir:          dir,
		sessionTS:    time.Now(),
		pollInterval: 3 * time.Second,
		writtenFiles: make(map[string]bool),
		stopCh:       make(chan struct{}),
	}
}

// Start запускает фоновую горутину детектора.
func (d *Detector) Start() {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		ticker := time.NewTicker(d.pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-d.stopCh:
				return
			case <-ticker.C:
				d.check()
			}
		}
	}()
}

// Stop останавливает детектор и ждёт завершения горутины.
func (d *Detector) Stop() {
	close(d.stopCh)
	d.wg.Wait()
}

// check просматривает новые события с момента последней проверки.
func (d *Detector) check() {
	d.mu.Lock()
	since := d.lastID
	d.mu.Unlock()

	events := d.evLog.GetSince(since)
	if len(events) == 0 {
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()

	// BUG FIX: группируем аномалии по виду, а не собираем всё в один срез.
	// Прежний код хранил единственную переменную kind, которую перезаписывал
	// каждый раз при обнаружении новой аномалии — в результате, если за один
	// тик появлялись и ERROR, и CRASH, оба события писались в один файл с
	// видом последнего обработанного события. Теперь каждый вид аномалии
	// записывается в отдельный файл (anomaly-..._error.log, ..._crash.log и т.д.).
	byKind := map[AnomalyKind][]eventlog.Event{}

	for _, e := range events {
		if e.ID > d.lastID {
			d.lastID = e.ID
		}

		switch e.Level {
		case eventlog.LevelError:
			byKind[KindError] = append(byKind[KindError], e)

		case eventlog.LevelWarn:
			// BUG FIX: crash-ключевые слова проверяем ДО burst-счётчика.
			// Прежний код проверял crash-сигнатуры только для LevelInfo,
			// хотя комментарий явно говорил «info/warn сообщениях sing-box».
			// Кроме того, warn с crash-ключевым словом не должен засчитываться
			// в burst-счётчик — это самостоятельный вид аномалии.
			if isCrashMessage(e.Message) {
				byKind[KindCrash] = append(byKind[KindCrash], e)
				break
			}

			// Отслеживаем burst: 3+ warn за 60 секунд
			d.warnTimes = append(d.warnTimes, e.Timestamp)
			cutoff := now.Add(-60 * time.Second)
			fresh := d.warnTimes[:0]
			for _, t := range d.warnTimes {
				if t.After(cutoff) {
					fresh = append(fresh, t)
				}
			}
			d.warnTimes = fresh
			if len(d.warnTimes) >= 3 {
				byKind[KindWarnBurst] = append(byKind[KindWarnBurst], e)
			}

		case eventlog.LevelInfo:
			// Детектируем краш по ключевым словам в info-сообщениях sing-box
			if isCrashMessage(e.Message) {
				byKind[KindCrash] = append(byKind[KindCrash], e)
			}
		}
	}

	for kind, anomalies := range byKind {
		d.writeFile(kind, anomalies)
	}
}

// isCrashMessage проверяет, содержит ли сообщение краш-сигнатуру sing-box.
func isCrashMessage(msg string) bool {
	lower := strings.ToLower(msg)
	for _, kw := range crashKeywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

// writeFile сохраняет диагностический файл.
// Имя файла: anomaly-YYYY-MM-DD_HH-MM-SS.log (по времени сессии + суффикс вида аномалии).
// Если файл с таким именем уже есть — добавляет события в конец (append).
func (d *Detector) writeFile(kind AnomalyKind, anomalies []eventlog.Event) {
	filename := fmt.Sprintf("anomaly-%s_%s.log",
		d.sessionTS.Format("2006-01-02_15-04-05"),
		strings.ToLower(string(kind)),
	)
	path := filepath.Join(d.dir, filename)

	isNew := !d.writtenFiles[filename]
	d.writtenFiles[filename] = true

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer func() { _ = f.Sync(); _ = f.Close() }()

	if isNew {
		// Заголовок файла
		fmt.Fprintf(f, "╔══════════════════════════════════════════════════════════╗\n")
		fmt.Fprintf(f, "║  ANOMALY LOG — %s\n", d.sessionTS.Format("2006-01-02 15:04:05"))
		fmt.Fprintf(f, "║  Тип: %s\n", kind)
		fmt.Fprintf(f, "╚══════════════════════════════════════════════════════════╝\n\n")

		// Контекст: последние 50 событий (чтобы понять что было до аномалии)
		context := d.evLog.GetSince(0)
		if len(context) > 50 {
			context = context[len(context)-50:]
		}
		if len(context) > 0 {
			fmt.Fprintf(f, "── КОНТЕКСТ (последние события) ──────────────────────────\n")
			for _, e := range context {
				fmt.Fprintf(f, "[%s] %-5s [%s] %s\n",
					e.Timestamp.Format("15:04:05.000"),
					strings.ToUpper(string(e.Level)),
					e.Source,
					e.Message,
				)
			}
			fmt.Fprintf(f, "\n")
		}
	}

	// Аномальные события
	fmt.Fprintf(f, "── АНОМАЛИЯ #%d [%s] ──────────────────────────────────────\n",
		len(d.writtenFiles), time.Now().Format("15:04:05"))
	for _, e := range anomalies {
		fmt.Fprintf(f, "[%s] %-5s [%s] %s\n",
			e.Timestamp.Format("15:04:05.000"),
			strings.ToUpper(string(e.Level)),
			e.Source,
			e.Message,
		)
	}
	fmt.Fprintf(f, "\n")
}
