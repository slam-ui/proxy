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

// New создаёт новый логгер
func New(cfg Config) Logger {
	if cfg.Output == nil {
		cfg.Output = os.Stdout
	}

	return &logger{
		level:  cfg.Level,
		logger: log.New(cfg.Output, "", 0),
	}
}

func (l *logger) log(level Level, format string, args ...interface{}) {
	if level < l.level {
		return
	}

	timestamp := time.Now().Format("2006-01-02 15:04:05")
	message := fmt.Sprintf(format, args...)
	l.logger.Printf("[%s] %s: %s", timestamp, level.String(), message)
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
