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
