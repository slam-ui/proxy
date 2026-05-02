package routing

import "testing"

func TestLoadPresets(t *testing.T) {
	presets, err := LoadPresets()
	if err != nil {
		t.Fatalf("LoadPresets: %v", err)
	}
	if len(presets) != 4 {
		t.Fatalf("presets len=%d, want 4", len(presets))
	}
	for _, preset := range presets {
		if preset.ID == "" || preset.Name == "" {
			t.Fatalf("preset has empty identity: %+v", preset)
		}
		if preset.DefaultAction == "" {
			t.Fatalf("preset %s has empty default action", preset.ID)
		}
		for _, rule := range preset.Rules {
			if rule.ID == "" || !rule.Enabled || len(rule.Match.Values) == 0 {
				t.Fatalf("preset %s has invalid rule: %+v", preset.ID, rule)
			}
		}
	}
}

func TestLoadPresetRejectsPathTraversal(t *testing.T) {
	if _, err := LoadPreset("../developer"); err == nil {
		t.Fatal("LoadPreset accepted path traversal id")
	}
}
