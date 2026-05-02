package hotkeys

import (
	"errors"
	"testing"
)

func TestParseAcceleratorCanonicalizes(t *testing.T) {
	got, err := ParseAccelerator("alt + ctrl + p")
	if err != nil {
		t.Fatalf("ParseAccelerator: %v", err)
	}
	if got.Canonical != "Ctrl+Alt+P" {
		t.Fatalf("canonical=%q, want Ctrl+Alt+P", got.Canonical)
	}
	if got.Modifiers != ModControl|ModAlt || got.Key != 'P' {
		t.Fatalf("parsed=%+v", got)
	}
}

func TestParseAcceleratorRejectsInvalid(t *testing.T) {
	for _, raw := range []string{"P", "Ctrl+Alt+P+S", "Ctrl+Alt+F13"} {
		if _, err := ParseAccelerator(raw); err == nil {
			t.Fatalf("ParseAccelerator(%q) returned nil error", raw)
		}
	}
}

func TestNormalizeSettingsAddsMissingDefaults(t *testing.T) {
	settings := NormalizeSettings(Settings{Enabled: true, Bindings: []Binding{{Action: ActionToggleConnection, Accelerator: "ctrl+alt+p", Enabled: true}}})
	if len(settings.Bindings) != len(DefaultSettings().Bindings) {
		t.Fatalf("bindings len=%d, want %d", len(settings.Bindings), len(DefaultSettings().Bindings))
	}
	if settings.Bindings[0].Accelerator != "Ctrl+Alt+P" {
		t.Fatalf("first accelerator=%q", settings.Bindings[0].Accelerator)
	}
}

func TestRegisterAllReportsConflicts(t *testing.T) {
	reg := fakeRegistrar{failID: 1}
	conflicts := RegisterAll(reg, Settings{Enabled: true, Bindings: []Binding{
		{Action: ActionToggleConnection, Accelerator: "Ctrl+Alt+P", Enabled: true},
		{Action: ActionNextServer, Accelerator: "Ctrl+Alt+S", Enabled: false},
	}})
	if len(conflicts) != 1 {
		t.Fatalf("conflicts len=%d, want 1: %+v", len(conflicts), conflicts)
	}
	if conflicts[0].Action != ActionToggleConnection {
		t.Fatalf("conflict action=%q", conflicts[0].Action)
	}
}

type fakeRegistrar struct {
	failID int
}

func (f fakeRegistrar) Register(id int, _ ParsedAccelerator) error {
	if id == f.failID {
		return errors.New("already registered")
	}
	return nil
}

func (f fakeRegistrar) Unregister(id int) error {
	return nil
}
