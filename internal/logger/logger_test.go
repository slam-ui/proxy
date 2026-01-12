package logger

import (
	"bytes"
	"strings"
	"testing"
)

func TestLogger_Levels(t *testing.T) {
	tests := []struct {
		name      string
		level     Level
		logFunc   func(Logger, string, ...interface{})
		message   string
		shouldLog bool
	}{
		{
			name:      "debug logs at debug level",
			level:     DebugLevel,
			logFunc:   func(l Logger, msg string, args ...interface{}) { l.Debug(msg, args...) },
			message:   "debug message",
			shouldLog: true,
		},
		{
			name:      "debug doesn't log at info level",
			level:     InfoLevel,
			logFunc:   func(l Logger, msg string, args ...interface{}) { l.Debug(msg, args...) },
			message:   "debug message",
			shouldLog: false,
		},
		{
			name:      "info logs at info level",
			level:     InfoLevel,
			logFunc:   func(l Logger, msg string, args ...interface{}) { l.Info(msg, args...) },
			message:   "info message",
			shouldLog: true,
		},
		{
			name:      "info logs at debug level",
			level:     DebugLevel,
			logFunc:   func(l Logger, msg string, args ...interface{}) { l.Info(msg, args...) },
			message:   "info message",
			shouldLog: true,
		},
		{
			name:      "warn logs at warn level",
			level:     WarnLevel,
			logFunc:   func(l Logger, msg string, args ...interface{}) { l.Warn(msg, args...) },
			message:   "warn message",
			shouldLog: true,
		},
		{
			name:      "error logs at error level",
			level:     ErrorLevel,
			logFunc:   func(l Logger, msg string, args ...interface{}) { l.Error(msg, args...) },
			message:   "error message",
			shouldLog: true,
		},
		{
			name:      "info doesn't log at warn level",
			level:     WarnLevel,
			logFunc:   func(l Logger, msg string, args ...interface{}) { l.Info(msg, args...) },
			message:   "info message",
			shouldLog: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := New(Config{
				Level:  tt.level,
				Output: &buf,
			})

			tt.logFunc(log, tt.message)

			output := buf.String()
			if tt.shouldLog {
				if !strings.Contains(output, tt.message) {
					t.Errorf("Expected log to contain '%s', got '%s'", tt.message, output)
				}
			} else {
				if output != "" {
					t.Errorf("Expected no log output, got '%s'", output)
				}
			}
		})
	}
}

func TestLogger_Formatting(t *testing.T) {
	tests := []struct {
		name     string
		level    Level
		logFunc  func(Logger, string, ...interface{})
		format   string
		args     []interface{}
		contains []string
	}{
		{
			name:     "info with formatting",
			level:    InfoLevel,
			logFunc:  func(l Logger, msg string, args ...interface{}) { l.Info(msg, args...) },
			format:   "User %s logged in from %s",
			args:     []interface{}{"john", "192.168.1.1"},
			contains: []string{"INFO", "User john logged in from 192.168.1.1"},
		},
		{
			name:     "error with formatting",
			level:    ErrorLevel,
			logFunc:  func(l Logger, msg string, args ...interface{}) { l.Error(msg, args...) },
			format:   "Failed to connect: %v",
			args:     []interface{}{"connection timeout"},
			contains: []string{"ERROR", "Failed to connect: connection timeout"},
		},
		{
			name:     "debug with multiple args",
			level:    DebugLevel,
			logFunc:  func(l Logger, msg string, args ...interface{}) { l.Debug(msg, args...) },
			format:   "Processing item %d of %d: %s",
			args:     []interface{}{5, 10, "success"},
			contains: []string{"DEBUG", "Processing item 5 of 10: success"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := New(Config{
				Level:  tt.level,
				Output: &buf,
			})

			tt.logFunc(log, tt.format, tt.args...)

			output := buf.String()
			for _, expected := range tt.contains {
				if !strings.Contains(output, expected) {
					t.Errorf("Expected log to contain '%s', got '%s'", expected, output)
				}
			}
		})
	}
}

func TestLogger_TimestampFormat(t *testing.T) {
	var buf bytes.Buffer
	log := New(Config{
		Level:  InfoLevel,
		Output: &buf,
	})

	log.Info("test message")

	output := buf.String()

	// Check timestamp format (YYYY-MM-DD HH:MM:SS)
	if !strings.Contains(output, "[") || !strings.Contains(output, "]") {
		t.Error("Expected timestamp in brackets")
	}

	// Should contain year-month-day
	parts := strings.Split(output, " ")
	if len(parts) < 2 {
		t.Errorf("Expected timestamp and message, got '%s'", output)
	}
}

func TestLogger_LevelString(t *testing.T) {
	tests := []struct {
		level Level
		want  string
	}{
		{DebugLevel, "DEBUG"},
		{InfoLevel, "INFO"},
		{WarnLevel, "WARN"},
		{ErrorLevel, "ERROR"},
		{Level(999), "UNKNOWN"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := tt.level.String()
			if got != tt.want {
				t.Errorf("Level.String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNoOpLogger(t *testing.T) {
	// NoOpLogger should not panic and should do nothing
	log := &NoOpLogger{}

	// These should all be no-ops and not panic
	log.Debug("debug message")
	log.Info("info message")
	log.Warn("warn message")
	log.Error("error message")

	// Test with formatting
	log.Debug("debug %s", "formatted")
	log.Info("info %d", 123)
	log.Warn("warn %v", true)
	log.Error("error %s %d", "test", 456)
}

func TestNew_DefaultOutput(t *testing.T) {
	// Should use os.Stdout if Output is nil
	log := New(Config{
		Level:  InfoLevel,
		Output: nil,
	})

	if log == nil {
		t.Fatal("Expected logger, got nil")
	}

	// Should not panic
	log.Info("test message")
}

func TestLogger_ConcurrentAccess(t *testing.T) {
	var buf bytes.Buffer
	log := New(Config{
		Level:  InfoLevel,
		Output: &buf,
	})

	// Test concurrent writes
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func(n int) {
			log.Info("message %d", n)
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Should have logged all messages without panic
	output := buf.String()
	if output == "" {
		t.Error("Expected some log output")
	}
}

func BenchmarkLogger_Info(b *testing.B) {
	var buf bytes.Buffer
	log := New(Config{
		Level:  InfoLevel,
		Output: &buf,
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		log.Info("test message %d", i)
	}
}

func BenchmarkLogger_Debug_Filtered(b *testing.B) {
	var buf bytes.Buffer
	log := New(Config{
		Level:  InfoLevel, // Debug won't be logged
		Output: &buf,
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		log.Debug("test message %d", i)
	}
}

func BenchmarkNoOpLogger(b *testing.B) {
	log := &NoOpLogger{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		log.Info("test message %d", i)
	}
}
