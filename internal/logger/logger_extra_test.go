package logger

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

// ─── Матрица фильтрации: полное покрытие всех пар уровень/метод ──────────

// TestLogger_FilterMatrix проверяет все 16 комбинаций (4 уровня логгера × 4 метода).
func TestLogger_FilterMatrix(t *testing.T) {
	cases := []struct {
		loggerLevel Level
		method      func(Logger, string)
		wantOutput  bool
	}{
		// Debug-метод
		{DebugLevel, func(l Logger, m string) { l.Debug(m) }, true},
		{InfoLevel, func(l Logger, m string) { l.Debug(m) }, false},
		{WarnLevel, func(l Logger, m string) { l.Debug(m) }, false},
		{ErrorLevel, func(l Logger, m string) { l.Debug(m) }, false},
		// Info-метод
		{DebugLevel, func(l Logger, m string) { l.Info(m) }, true},
		{InfoLevel, func(l Logger, m string) { l.Info(m) }, true},
		{WarnLevel, func(l Logger, m string) { l.Info(m) }, false},
		{ErrorLevel, func(l Logger, m string) { l.Info(m) }, false},
		// Warn-метод
		{DebugLevel, func(l Logger, m string) { l.Warn(m) }, true},
		{InfoLevel, func(l Logger, m string) { l.Warn(m) }, true},
		{WarnLevel, func(l Logger, m string) { l.Warn(m) }, true},
		{ErrorLevel, func(l Logger, m string) { l.Warn(m) }, false},
		// Error-метод
		{DebugLevel, func(l Logger, m string) { l.Error(m) }, true},
		{InfoLevel, func(l Logger, m string) { l.Error(m) }, true},
		{WarnLevel, func(l Logger, m string) { l.Error(m) }, true},
		{ErrorLevel, func(l Logger, m string) { l.Error(m) }, true},
	}
	for _, tc := range cases {
		var buf bytes.Buffer
		l := New(Config{Level: tc.loggerLevel, Output: &buf})
		tc.method(l, "probe")
		got := buf.Len() > 0
		if got != tc.wantOutput {
			t.Errorf("loggerLevel=%v: got output=%v, wantOutput=%v", tc.loggerLevel, got, tc.wantOutput)
		}
	}
}

// ─── Формат вывода ────────────────────────────────────────────────────────

// TestLogger_OutputContainsLevelLabel проверяет что каждый метод пишет
// соответствующий label (DEBUG/INFO/WARN/ERROR) в output.
func TestLogger_OutputContainsLevelLabel(t *testing.T) {
	methods := []struct {
		name  string
		call  func(Logger)
		label string
	}{
		{"Debug", func(l Logger) { l.Debug("msg") }, "DEBUG"},
		{"Info", func(l Logger) { l.Info("msg") }, "INFO"},
		{"Warn", func(l Logger) { l.Warn("msg") }, "WARN"},
		{"Error", func(l Logger) { l.Error("msg") }, "ERROR"},
	}
	for _, tc := range methods {
		var buf bytes.Buffer
		l := New(Config{Level: DebugLevel, Output: &buf})
		tc.call(l)
		if !strings.Contains(buf.String(), tc.label) {
			t.Errorf("[%s] output не содержит label %q: %q", tc.name, tc.label, buf.String())
		}
	}
}

// TestLogger_OutputContainsTimestampBrackets проверяет формат метки времени.
func TestLogger_OutputContainsTimestampBrackets(t *testing.T) {
	var buf bytes.Buffer
	l := New(Config{Level: InfoLevel, Output: &buf})
	l.Info("ts test")
	output := buf.String()
	if !strings.Contains(output, "[") || !strings.Contains(output, "]") {
		t.Errorf("output должен содержать метку времени в скобках: %q", output)
	}
}

// TestLogger_NoSprintf_ArtifactsOnNoArgs проверяет что при вызове без args
// строка не проходит через Sprintf (нет %!(EXTRA) или %!(NOVERB)).
func TestLogger_NoSprintf_ArtifactsOnNoArgs(t *testing.T) {
	var buf bytes.Buffer
	l := New(Config{Level: InfoLevel, Output: &buf})
	l.Info("loaded 100%% of config")
	output := buf.String()
	if strings.Contains(output, "EXTRA") || strings.Contains(output, "%!") {
		t.Errorf("форматирование без args дало артефакты: %q", output)
	}
}

// ─── Level.String() ────────────────────────────────────────────────────────

func TestLevel_String_AllKnown(t *testing.T) {
	cases := []struct {
		l    Level
		want string
	}{
		{DebugLevel, "DEBUG"},
		{InfoLevel, "INFO"},
		{WarnLevel, "WARN"},
		{ErrorLevel, "ERROR"},
	}
	for _, tc := range cases {
		if got := tc.l.String(); got != tc.want {
			t.Errorf("Level(%d).String() = %q, want %q", tc.l, got, tc.want)
		}
	}
}

func TestLevel_String_UnknownValues(t *testing.T) {
	unknowns := []Level{Level(-1), Level(5), Level(100)}
	for _, l := range unknowns {
		if got := l.String(); got != "UNKNOWN" {
			t.Errorf("Level(%d).String() = %q, want UNKNOWN", l, got)
		}
	}
}

// ─── Параллельная безопасность ───────────────────────────────────────────

func TestLogger_ConcurrentAllMethods(t *testing.T) {
	var buf syncBuffer
	l := New(Config{Level: DebugLevel, Output: &buf})

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(4)
		go func(n int) { defer wg.Done(); l.Debug("debug %d", n) }(i)
		go func(n int) { defer wg.Done(); l.Info("info %d", n) }(i)
		go func(n int) { defer wg.Done(); l.Warn("warn %d", n) }(i)
		go func(n int) { defer wg.Done(); l.Error("error %d", n) }(i)
	}
	wg.Wait()

	if buf.Len() == 0 {
		t.Error("параллельные записи должны дать непустой output")
	}
}

// ─── Benchmark ────────────────────────────────────────────────────────────

func BenchmarkLogger_Info_WithFormat(b *testing.B) {
	var buf bytes.Buffer
	l := New(Config{Level: InfoLevel, Output: &buf})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l.Info("request completed: status=%d latency=%dms", 200, i%100)
	}
}

func BenchmarkLogger_Info_NoArgs(b *testing.B) {
	var buf bytes.Buffer
	l := New(Config{Level: InfoLevel, Output: &buf})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l.Info("server started")
	}
}

func BenchmarkLogger_AllLevels(b *testing.B) {
	var buf bytes.Buffer
	l := New(Config{Level: DebugLevel, Output: &buf})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l.Debug("debug msg")
		l.Info("info msg")
		l.Warn("warn msg")
		l.Error("error msg")
	}
}

// ─── helper ───────────────────────────────────────────────────────────────

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Len()
}
