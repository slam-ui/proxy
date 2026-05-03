package onboarding

import (
	"os"
	"path/filepath"
	"testing"

	"proxyclient/internal/config"
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

func TestApplySmartDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "settings.json")
	settings := config.DefaultAppSettings()
	settings.StartProxyOnLaunch = true
	settings.Updates.Enabled = false
	settings.Updates.Channel = "beta"
	settings.LeakTest.Enabled = false
	if err := config.SaveAppSettings(path, settings); err != nil {
		t.Fatalf("SaveAppSettings: %v", err)
	}
	got, err := ApplySmartDefaults(path)
	if err != nil {
		t.Fatalf("ApplySmartDefaults returned error: %v", err)
	}
	if got.StartProxyOnLaunch {
		t.Fatal("StartProxyOnLaunch=true, want false")
	}
	if !got.Updates.Enabled || got.Updates.Channel != "stable" {
		t.Fatalf("updates=%+v, want enabled stable", got.Updates)
	}
	if !got.LeakTest.Enabled {
		t.Fatal("LeakTest.Enabled=false, want true")
	}
}
