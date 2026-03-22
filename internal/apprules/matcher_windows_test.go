//go:build windows

package apprules

import (
	"testing"
)

// Тесты в этом файле проверяют поведение специфичное для Windows:
// filepath.ToSlash конвертирует \ → / только на Windows.
// На Linux filepath.ToSlash — no-op, поэтому эти тесты исключены из Linux-сборки.

// TestNormalizePattern_ConvertBackslashToSlash проверяет что NormalizePattern
// конвертирует обратные слеши в прямые (Windows-поведение filepath.ToSlash).
func TestNormalizePattern_ConvertBackslashToSlash(t *testing.T) {
	cases := []struct{ in, want string }{
		{`C:\Program Files\app.exe`, "c:/program files/app.exe"},
		{`C:\Windows\System32\cmd.exe`, "c:/windows/system32/cmd.exe"},
		{`app.exe`, "app.exe"}, // нет слешей — без изменений
	}
	for _, tc := range cases {
		got := NormalizePattern(tc.in)
		if got != tc.want {
			t.Errorf("NormalizePattern(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestMatcher_FullWindowsPath_AsPattern проверяет что полный Windows-путь
// в позиции паттерна совпадает с таким же значением.
// На Linux filepath.Base не знает о \ как разделителе, поэтому тест только для Windows.
func TestMatcher_FullWindowsPath_AsPattern(t *testing.T) {
	m := NewMatcher()
	// После нормализации оба пути становятся c:/windows/system32/cmd.exe
	pattern := NormalizePattern(`C:\Windows\System32\cmd.exe`)
	value := `C:\Windows\System32\cmd.exe`
	if !m.Match(pattern, value) {
		t.Errorf("Match(%q, %q) = false, want true", pattern, value)
	}
}
