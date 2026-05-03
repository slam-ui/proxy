package crashreport

import (
	"strings"
	"testing"
)

func TestSanitizeRemovesSensitiveTokens(t *testing.T) {
	in := "uuid 123e4567-e89b-12d3-a456-426614174000 ip 192.168.1.25 token abcdefghijklmnopqrstuvwxyz0123456789"
	got := Sanitize(in)
	for _, forbidden := range []string{
		"123e4567-e89b-12d3-a456-426614174000",
		"192.168.1.25",
		"abcdefghijklmnopqrstuvwxyz0123456789",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("Sanitize leaked %q in %q", forbidden, got)
		}
	}
}

func TestSanitizedForUploadClearsMemoryAndSanitizesFields(t *testing.T) {
	report := &CrashReport{
		LastOutput: "connect 10.0.0.1",
		ConfigSafe: "id 123e4567-e89b-12d3-a456-426614174000",
		ErrorMsg:   "token abcdefghijklmnopqrstuvwxyz0123456789",
		MemoryMB:   512,
	}
	got := report.SanitizedForUpload()
	if got.MemoryMB != 0 {
		t.Fatalf("MemoryMB=%d, want 0", got.MemoryMB)
	}
	for _, field := range []string{got.LastOutput, got.ConfigSafe, got.ErrorMsg} {
		if strings.Contains(field, "10.0.0.1") ||
			strings.Contains(field, "123e4567-e89b-12d3-a456-426614174000") ||
			strings.Contains(field, "abcdefghijklmnopqrstuvwxyz0123456789") {
			t.Fatalf("upload field leaked sensitive data: %q", field)
		}
	}
}
