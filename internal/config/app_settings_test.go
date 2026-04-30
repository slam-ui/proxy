package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAppSettings_MissingFileUsesDefault(t *testing.T) {
	settings, err := LoadAppSettings(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatalf("LoadAppSettings returned error: %v", err)
	}
	if !settings.StartProxyOnLaunch {
		t.Error("StartProxyOnLaunch default should be true")
	}
}

func TestLoadAppSettings_EmptyJSONKeepsDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	settings, err := LoadAppSettings(path)
	if err != nil {
		t.Fatalf("LoadAppSettings returned error: %v", err)
	}
	if !settings.StartProxyOnLaunch {
		t.Error("empty JSON should keep StartProxyOnLaunch default true")
	}
}

func TestSaveAndLoadAppSettings_FalseValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "settings.json")
	want := AppSettings{StartProxyOnLaunch: false}
	if err := SaveAppSettings(path, want); err != nil {
		t.Fatalf("SaveAppSettings returned error: %v", err)
	}
	got, err := LoadAppSettings(path)
	if err != nil {
		t.Fatalf("LoadAppSettings returned error: %v", err)
	}
	if got.StartProxyOnLaunch {
		t.Error("StartProxyOnLaunch should persist false")
	}
}

func TestSaveAppSettings_NormalizesBeforeWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "settings.json")
	settings := AppSettings{
		StartProxyOnLaunch: false,
		TrafficBudget: TrafficBudgetSettings{
			WarnPercent: 999,
		},
		SmartFailover: SmartFailoverSettings{
			CheckIntervalSec: 1,
			MaxLatencyMs:     -1,
			MinImprovementMs: -1,
		},
	}
	if err := SaveAppSettings(path, settings); err != nil {
		t.Fatalf("SaveAppSettings returned error: %v", err)
	}
	got, err := LoadAppSettings(path)
	if err != nil {
		t.Fatalf("LoadAppSettings returned error: %v", err)
	}
	if got.TrafficBudget.WarnPercent != 80 {
		t.Fatalf("WarnPercent = %d, want 80", got.TrafficBudget.WarnPercent)
	}
	if got.SmartFailover.CheckIntervalSec != 60 {
		t.Fatalf("CheckIntervalSec = %d, want 60", got.SmartFailover.CheckIntervalSec)
	}
	if got.SmartFailover.MaxLatencyMs != 800 {
		t.Fatalf("MaxLatencyMs = %d, want 800", got.SmartFailover.MaxLatencyMs)
	}
	if got.SmartFailover.MinImprovementMs != 50 {
		t.Fatalf("MinImprovementMs = %d, want 50", got.SmartFailover.MinImprovementMs)
	}
}
