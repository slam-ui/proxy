package dpapi

import (
	"bytes"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	plain := []byte("safesky dpapi test secret")

	encrypted, err := Encrypt(plain)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	decrypted, err := Decrypt(encrypted)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(decrypted, plain) {
		t.Fatalf("Decrypt(Encrypt(plain)) = %q, want %q", decrypted, plain)
	}
}
