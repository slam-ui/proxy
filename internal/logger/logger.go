package logger

import (
	"fmt"
	"io"
	"log"
	"os"
	"time"
)

// Level представляет уровень логирования
type Level int

const (
	DebugLevel Level = iota
	InfoLevel
	WarnLevel
	ErrorLevel
)

func (l Level) String() string {
	switch l {
	case DebugLevel:
		return "DEBUG"
	case InfoLevel:
		return "INFO"
	case WarnLevel:
		return "WARN"
	case ErrorLevel:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

// Logger интерфейс для логирования
type Logger interface {
	Debug(format string, args ...interface{})
	Info(format string, args ...interface{})
	Warn(format string, args ...interface{})
	Error(format string, args ...interface{})
}

// Config конфигурация логгера
type Config struct {
	Level  Level
	Output io.Writer
}

// logger реализация Logger
type logger struct {
	level  Level
	logger *log.Logger
}

// New создаёт новый логгер.
// Принимает Config или Level (для краткости в тестах):
//
//	logger.New(logger.Config{Level: logger.InfoLevel})
//	logger.New(logger.LevelInfo)   // тестовая форма
func New(arg interface{}) Logger {
	var cfg Config
	switch v := arg.(type) {
	case Config:
		cfg = v
	case Level:
		cfg = Config{Level: v}
	default:
		cfg = Config{}
	}
	if cfg.Output == nil {
		cfg.Output = os.Stdout
	}
	return &logger{
		level:  cfg.Level,
		logger: log.New(cfg.Output, "", 0),
	}
}

func (l *logger) log(level Level, format string, args ...interface{}) {
	// OPT #2: проверяем уровень ДО форматирования — Sprintf не вызывается
	// если сообщение будет отброшено. Также пропускаем Sprintf когда нет
	// аргументов (format уже готовая строка без %v/%s/...).
	if level < l.level {
		return
	}
	var msg string
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	} else {
		msg = format
	}
	timestamp := time.Now().Format("15:04:05.000")
	l.logger.Printf("[%s] %-5s %s", timestamp, level.String(), msg)
}

func (l *logger) Debug(format string, args ...interface{}) {
	l.log(DebugLevel, format, args...)
}

func (l *logger) Info(format string, args ...interface{}) {
	l.log(InfoLevel, format, args...)
}

func (l *logger) Warn(format string, args ...interface{}) {
	l.log(WarnLevel, format, args...)
}

func (l *logger) Error(format string, args ...interface{}) {
	l.log(ErrorLevel, format, args...)
}

// NoOpLogger логгер-заглушка для тестов
type NoOpLogger struct{}

func (n *NoOpLogger) Debug(_ string, _ ...interface{}) {}
func (n *NoOpLogger) Info(_ string, _ ...interface{})  {}
func (n *NoOpLogger) Warn(_ string, _ ...interface{})  {}
func (n *NoOpLogger) Error(_ string, _ ...interface{}) {}

// NewNop возвращает логгер-заглушку, который молча отбрасывает все сообщения.
// Удобен в тестах, где вывод логов не нужен.
func NewNop() Logger {
	return &NoOpLogger{}
}

// NewWithLevel - удобная функция для создания логгера с указанным уровнем.
// Используется в тестах для упрощения синтаксиса.
func NewWithLevel(level Level) Logger {
	return New(Config{Level: level})
}

// Exported level aliases — используются в тестах как logger.LevelInfo и т.д.
const (
	LevelDebug = DebugLevel
	LevelInfo  = InfoLevel
	LevelWarn  = WarnLevel
	LevelError = ErrorLevel
)
