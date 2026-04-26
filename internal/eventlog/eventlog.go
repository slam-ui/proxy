package eventlog

import (
	"bytes"
	"fmt"
	"sync"
	"time"

	"proxyclient/internal/logger"
)

// Level уровень события
type Level string

const (
	LevelDebug Level = "debug"
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

// Event одно событие в кольцевом буфере
type Event struct {
	ID        int       `json:"id"`
	Timestamp time.Time `json:"ts"`
	Level     Level     `json:"level"`
	Source    string    `json:"source"`
	Message   string    `json:"message"`
}

// Log — потокобезопасный кольцевой буфер событий
type Log struct {
	mu      sync.RWMutex
	events  []Event
	maxSize int
	counter int64
	// head — индекс самого старого элемента
	head int
	// size — текущее число заполненных слотов
	size int
}

// New создаёт буфер на maxSize событий
func New(maxSize int) *Log {
	if maxSize <= 0 {
		maxSize = 1
	}
	return &Log{
		maxSize: maxSize,
		events:  make([]Event, maxSize),
	}
}

// Add добавляет событие. O(1) кольцевой буфер.
func (l *Log) Add(level Level, source, format string, args ...interface{}) {
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.counter++
	e := Event{
		ID:        int(l.counter),
		Timestamp: time.Now(),
		Level:     level,
		Source:    source,
		Message:   msg,
	}

	if l.size < l.maxSize {
		// Буфер не полон: пишем в конец (логический индекс: head + size)
		l.events[(l.head+l.size)%l.maxSize] = e
		l.size++
	} else {
		// Буфер полон: перезаписываем по индексу head (самый старый)
		l.events[l.head] = e
		l.head = (l.head + 1) % l.maxSize
	}
}

// GetSince возвращает события с ID > since в хронологическом порядке.
func (l *Log) GetSince(since int) []Event {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if l.size == 0 {
		return []Event{}
	}

	// ФИКС: Если счетчик был сброшен или ID стали меньше since (после очень большого перерыва),
	// и при этом с момента сброса что-то добавилось — логика бинарного поиска ниже справится.
	// Но для надежности: если самое старое событие уже новее чем since, отдаем всё.
	if l.events[l.head].ID > since {
		result := make([]Event, l.size)
		for i := 0; i < l.size; i++ {
			result[i] = l.events[(l.head+i)%l.maxSize]
		}
		return result
	}

	// Бинарный поиск первого элемента > since
	lo, hi := 0, l.size
	for lo < hi {
		mid := (lo + hi) / 2
		if l.events[(l.head+mid)%l.maxSize].ID <= since {
			lo = mid + 1
		} else {
			hi = mid
		}
	}

	count := l.size - lo
	if count <= 0 {
		return []Event{}
	}

	result := make([]Event, count)
	for i := 0; i < count; i++ {
		result[i] = l.events[(l.head+lo+i)%l.maxSize]
	}
	return result
}

// GetLatestID возвращает ID последнего добавленного события.
func (l *Log) GetLatestID() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	// ФИКС: Если лог пуст (после Clear), возвращаем 0.
	// Это удовлетворит тест TestHandleEventsClear_ClearsLog в API.
	if l.size == 0 {
		return 0
	}
	return int(l.counter)
}

// Clear полностью очищает буфер.
func (l *Log) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()

	for i := range l.events {
		l.events[i] = Event{}
	}

	l.head = 0
	l.size = 0
	// ФИКС: Мы НЕ сбрасываем l.counter = 0 здесь.
	// Счетчик должен продолжать расти, чтобы соблюдалась монотонность ID.
}

// ─── Logger adapter ──────────────────────────────────────────────────────────

// Logger реализует дублирование вывода: в консольный логгер и в кольцевой буфер.
type Logger struct {
	inner  logger.Logger
	evLog  *Log
	source string
}

func NewLogger(inner logger.Logger, evLog *Log, source string) *Logger {
	return &Logger{inner: inner, evLog: evLog, source: source}
}

// OPT: Форматируем строку один раз (msg), чтобы не вызывать Sprintf дважды.

func (l *Logger) Debug(format string, args ...interface{}) {
	msg := formatMsg(format, args...)
	l.inner.Debug("%s", msg)
	l.evLog.Add(LevelDebug, l.source, "%s", msg)
}

func (l *Logger) Info(format string, args ...interface{}) {
	msg := formatMsg(format, args...)
	l.inner.Info("%s", msg)
	l.evLog.Add(LevelInfo, l.source, "%s", msg)
}

func (l *Logger) Warn(format string, args ...interface{}) {
	msg := formatMsg(format, args...)
	l.inner.Warn("%s", msg)
	l.evLog.Add(LevelWarn, l.source, "%s", msg)
}

func (l *Logger) Error(format string, args ...interface{}) {
	msg := formatMsg(format, args...)
	l.inner.Error("%s", msg)
	l.evLog.Add(LevelError, l.source, "%s", msg)
}

func formatMsg(format string, args ...interface{}) string {
	if len(args) == 0 {
		return format
	}
	return fmt.Sprintf(format, args...)
}

// ─── LineWriter ───────────────────────────────────────────────────────────────

// LineWriter перехватывает поток байт (например, от sing-box) и пишет его в Log.
type LineWriter struct {
	mu     sync.Mutex
	buf    []byte
	evLog  *Log
	source string
	level  Level
}

func NewLineWriter(evLog *Log, source string, level Level) *LineWriter {
	return &LineWriter{evLog: evLog, source: source, level: level}
}

func (w *LineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		// Обрезаем лишние пробелы и возвраты каретки для чистоты лога
		line := string(bytes.TrimRight(w.buf[:i], "\r\t "))
		if line != "" {
			w.evLog.Add(w.level, w.source, "%s", line)
		}
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}
