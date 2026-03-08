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
	counter int
}

// New создаёт буфер на maxSize событий
func New(maxSize int) *Log {
	cap0 := maxSize
	if cap0 > 64 {
		cap0 = 64
	}
	return &Log{
		maxSize: maxSize,
		events:  make([]Event, 0, cap0),
	}
}

// Add добавляет событие; старые вытесняются при переполнении
func (l *Log) Add(level Level, source, format string, args ...interface{}) {
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.counter++
	e := Event{
		ID:        l.counter,
		Timestamp: time.Now(),
		Level:     level,
		Source:    source,
		Message:   msg,
	}

	l.events = append(l.events, e)
	if len(l.events) > l.maxSize {
		// Сдвигаем: удаляем самое старое событие
		copy(l.events, l.events[1:])
		l.events = l.events[:l.maxSize]
	}
}

// GetSince возвращает события с ID > since (в хронологическом порядке)
func (l *Log) GetSince(since int) []Event {
	l.mu.RLock()
	defer l.mu.RUnlock()

	result := make([]Event, 0)
	for _, e := range l.events {
		if e.ID > since {
			result = append(result, e)
		}
	}
	return result
}

// GetLatestID возвращает ID последнего добавленного события
func (l *Log) GetLatestID() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.counter
}

// Clear очищает буфер (ID-счётчик не сбрасывается)
func (l *Log) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = l.events[:0]
}

// ─── Logger adapter ──────────────────────────────────────────────────────────

// Logger реализует logger.Logger: пишет и в оригинальный логгер, и в кольцевой буфер
type Logger struct {
	inner  logger.Logger
	evLog  *Log
	source string
}

// NewLogger создаёт адаптер, который дублирует вывод в event log
func NewLogger(inner logger.Logger, evLog *Log, source string) *Logger {
	return &Logger{inner: inner, evLog: evLog, source: source}
}

func (l *Logger) Debug(format string, args ...interface{}) {
	l.inner.Debug(format, args...)
	l.evLog.Add(LevelDebug, l.source, format, args...)
}

func (l *Logger) Info(format string, args ...interface{}) {
	l.inner.Info(format, args...)
	l.evLog.Add(LevelInfo, l.source, format, args...)
}

func (l *Logger) Warn(format string, args ...interface{}) {
	l.inner.Warn(format, args...)
	l.evLog.Add(LevelWarn, l.source, format, args...)
}

func (l *Logger) Error(format string, args ...interface{}) {
	l.inner.Error(format, args...)
	l.evLog.Add(LevelError, l.source, format, args...)
}

// ─── LineWriter ───────────────────────────────────────────────────────────────

// LineWriter реализует io.Writer: буферизует вывод по строкам и пишет их в event log.
// Используется для захвата stdout/stderr sing-box процесса.
type LineWriter struct {
	mu     sync.Mutex
	buf    []byte
	evLog  *Log
	source string
	level  Level
}

// NewLineWriter создаёт io.Writer, разбивающий поток байт на строки и добавляющий в Log
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
		line := string(bytes.TrimRight(w.buf[:i], "\r\t "))
		if line != "" {
			w.evLog.Add(w.level, w.source, "%s", line)
		}
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}
