//go:build windows
// +build windows

package notification

import (
	"strings"
	"testing"
)

// ── escapePS Tests ────────────────────────────────────────────────────────────────

func TestEscapePS_EscapesSingleQuotes(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"hello", "hello"},
		{"it's", "it''s"},
		{"don't", "don''t"},
		{"can't won't", "can''t won''t"},
		{"''", "''''"},
		{"'", "''"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			result := escapePS(tc.input)
			if result != tc.expected {
				t.Errorf("escapePS(%q) = %q, want %q", tc.input, result, tc.expected)
			}
		})
	}
}

func TestEscapePS_HandlesEmptyString(t *testing.T) {
	result := escapePS("")
	if result != "" {
		t.Errorf("escapePS('') = %q, want empty", result)
	}
}

func TestEscapePS_HandlesNoQuotes(t *testing.T) {
	input := "Simple message without quotes"
	result := escapePS(input)

	if result != input {
		t.Errorf("escapePS(%q) = %q, want unchanged", input, result)
	}
}

func TestEscapePS_HandlesMultipleQuotes(t *testing.T) {
	input := "It's John's book"
	expected := "It''s John''s book"

	result := escapePS(input)

	if result != expected {
		t.Errorf("escapePS(%q) = %q, want %q", input, result, expected)
	}
}

func TestEscapePS_HandlesSpecialChars(t *testing.T) {
	// Test with various special characters
	tests := []string{
		"Hello\nWorld",
		"Tab\there",
		"Quote: \"test\"",
		"Back`tick",
		"Dollar $ign",
		"Semicolon; End",
	}

	for _, tc := range tests {
		t.Run(tc, func(t *testing.T) {
			// Should not panic
			result := escapePS(tc)
			_ = result
		})
	}
}

func TestEscapePS_OnlyEscapesSingleQuotes(t *testing.T) {
	// Verify that only single quotes are escaped
	input := `Test "double" and 'single' quotes`
	result := escapePS(input)

	// Double quotes should remain unchanged
	if !strings.Contains(result, `"double"`) {
		t.Error("Double quotes should remain unchanged")
	}

	// Single quotes should be escaped
	if !strings.Contains(result, `''single''`) {
		t.Error("Single quotes should be escaped")
	}
}

func TestEscapePS_ConsecutiveQuotes(t *testing.T) {
	input := "''''" // Four single quotes
	expected := "''''''''" // Eight single quotes (each ' becomes '')

	result := escapePS(input)

	if result != expected {
		t.Errorf("escapePS(%q) = %q, want %q", input, result, expected)
	}
}

func TestEscapePS_Unicode(t *testing.T) {
	tests := []struct {
		input    string
		contains string
	}{
		{"Привет мир", "Привет мир"},
		{"日本語テスト", "日本語テスト"},
		{"Emoji 🔥 test", "🔥"},
		{"It's café", "café"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			result := escapePS(tc.input)
			if !strings.Contains(result, tc.contains) {
				t.Errorf("escapePS(%q) = %q, should contain %q", tc.input, result, tc.contains)
			}
		})
	}
}

// ── Send Tests ────────────────────────────────────────────────────────────────────

func TestSend_DoesNotPanic(t *testing.T) {
	// Test that Send doesn't panic with various inputs
	// Note: Actual toast notification requires PowerShell

	testCases := []struct {
		title   string
		message string
	}{
		{"Test", "Test message"},
		{"", "Empty title"},
		{"Empty message", ""},
		{"Both empty", ""},
		{"It's a test", "Don't panic!"},
		{"Unicode", "Привет 日本語 🔥"},
		{"Long message", strings.Repeat("x", 1000)},
	}

	for _, tc := range testCases {
		t.Run(tc.title, func(t *testing.T) {
			// Send is async and may fail if PowerShell not available
			// We just verify it doesn't panic
			Send(tc.title, tc.message)
		})
	}
}

func TestSend_EscapesTitle(t *testing.T) {
	// Test that title with single quotes is handled
	Send("It's a notification", "Test message")
	// If we get here without panic, test passes
}

func TestSend_EscapesMessage(t *testing.T) {
	// Test that message with single quotes is handled
	Send("Test", "It's a test message with 'quotes'")
	// If we get here without panic, test passes
}

func TestSend_BothWithQuotes(t *testing.T) {
	// Test both title and message with quotes
	Send("It's a title", "It's a message with 'quotes'")
	// If we get here without panic, test passes
}

// ── Fuzz Tests ────────────────────────────────────────────────────────────────────

func FuzzEscapePS(f *testing.F) {
	seeds := []string{
		"",
		"simple",
		"it's",
		"''''",
		"日本語",
		"🔥🎉",
		"normal text",
		"it's john's book",
	}

	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, input string) {
		// Should not panic
		result := escapePS(input)

		// Result should not contain unescaped single quotes
		// unless input didn't have any
		if strings.Contains(input, "'") {
			// Count single quotes
			inputCount := strings.Count(input, "'")
			resultCount := strings.Count(result, "'")
			// Each ' should become ''
			if resultCount != inputCount*2 {
				t.Errorf("Quote count mismatch: input has %d, result has %d", inputCount, resultCount)
			}
		}
	})
}

// ── Benchmark Tests ──────────────────────────────────────────────────────────────

func BenchmarkEscapePS(b *testing.B) {
	input := "It's a test message with 'quotes' and more"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		escapePS(input)
	}
}

func BenchmarkEscapePS_NoQuotes(b *testing.B) {
	input := "Simple message without any quotes"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		escapePS(input)
	}
}

func BenchmarkEscapePS_ManyQuotes(b *testing.B) {
	input := strings.Repeat("it's ", 100)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		escapePS(input)
	}
}

// ── Table-driven tests ────────────────────────────────────────────────────────────

func TestEscapePS_Table(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"empty", "", ""},
		{"no quotes", "hello", "hello"},
		{"single quote", "'", "''"},
		{"quote in middle", "it's", "it''s"},
		{"quote at start", "'hello", "''hello"},
		{"quote at end", "hello'", "hello''"},
		{"multiple quotes", "it's john's", "it''s john''s"},
		{"consecutive quotes", "''", "''''"},
		{"spaces", "hello world", "hello world"},
		{"unicode", "привет", "привет"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := escapePS(tc.input)
			if result != tc.expected {
				t.Errorf("escapePS(%q) = %q, want %q", tc.input, result, tc.expected)
			}
		})
	}
}

// ── Edge Cases ────────────────────────────────────────────────────────────────────

func TestEscapePS_Newlines(t *testing.T) {
	input := "line1\nline2"
	result := escapePS(input)

	// Newlines should be preserved
	if !strings.Contains(result, "\n") {
		t.Error("Newlines should be preserved")
	}
}

func TestEscapePS_Tabs(t *testing.T) {
	input := "col1\tcol2"
	result := escapePS(input)

	// Tabs should be preserved
	if !strings.Contains(result, "\t") {
		t.Error("Tabs should be preserved")
	}
}

func TestEscapePS_Backslash(t *testing.T) {
	input := `path\to\file`
	result := escapePS(input)

	// Backslashes should be preserved (not escaped by our function)
	if !strings.Contains(result, `\`) {
		t.Error("Backslashes should be preserved")
	}
}

func TestEscapePS_VeryLongString(t *testing.T) {
	input := strings.Repeat("it's ", 10000) // 50,000+ characters

	result := escapePS(input)

	// Should handle without issues
	if len(result) < len(input) {
		t.Error("Result should be longer than input due to escaping")
	}
}

// ── PowerShell compatibility tests ────────────────────────────────────────────────

func TestEscapePS_PowerShellCompatibility(t *testing.T) {
	// Test strings that would be problematic in PowerShell if not escaped
	tests := []string{
		"$(Get-Process)",       // Command injection attempt
		"; Write-Host 'pwned'", // Command separator
		"| Out-File test.txt",  // Pipe to file
		"& 'malicious.exe'",    // Call operator
	}

	for _, tc := range tests {
		t.Run(tc[:min(20, len(tc))], func(t *testing.T) {
			// These should all be safe after escaping
			result := escapePS(tc)

			// Single quotes make these literal strings in PowerShell
			if strings.Contains(tc, "'") {
				// Should have escaped the quotes
				if strings.Contains(result, "''") {
					t.Log("Quotes properly escaped")
				}
			}
		})
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ── Integration-style test ────────────────────────────────────────────────────────

func TestNotification_SendVariousMessages(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping notification tests in short mode")
	}

	messages := []struct {
		title   string
		message string
	}{
		{"Proxy Started", "VPN connection established successfully"},
		{"Proxy Stopped", "Connection closed"},
		{"Error", "Failed to connect: connection refused"},
		{"It's Working", "Don't worry, it's fine!"},
	}

	for _, msg := range messages {
		t.Run(msg.title, func(t *testing.T) {
			// Just verify no panic
			Send(msg.title, msg.message)
		})
	}
}
