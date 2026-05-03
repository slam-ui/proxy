package onboarding

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCurrentMissingMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "onboarded")
	status, err := Current(path)
	if err != nil {
		t.Fatalf("Current returned error: %v", err)
	}
	if status.Onboarded {
		t.Fatal("Onboarded=true, want false")
	}
}

func TestMarkCompleteCreatesMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "onboarded")
	if err := MarkComplete(path); err != nil {
		t.Fatalf("MarkComplete returned error: %v", err)
	}
	status, err := Current(path)
	if err != nil {
		t.Fatalf("Current returned error: %v", err)
	}
	if !status.Onboarded {
		t.Fatal("Onboarded=false, want true")
	}
	if data, err := os.ReadFile(path); err != nil || len(data) == 0 {
		t.Fatalf("marker data len=%d err=%v, want non-empty", len(data), err)
	}
}
