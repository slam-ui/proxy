package routing

import (
	"embed"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed presets/*.json
var presetFiles embed.FS

type Preset struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	Description   string        `json:"description"`
	DefaultAction string        `json:"default_action"`
	Rules         []RoutingRule `json:"rules"`
}

func LoadPresets() ([]Preset, error) {
	entries, err := presetFiles.ReadDir("presets")
	if err != nil {
		return nil, fmt.Errorf("read routing presets: %w", err)
	}
	presets := make([]Preset, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		preset, err := loadPresetFile("presets/" + entry.Name())
		if err != nil {
			return nil, err
		}
		presets = append(presets, preset)
	}
	sort.SliceStable(presets, func(i, j int) bool { return presets[i].ID < presets[j].ID })
	return presets, nil
}

func LoadPreset(id string) (Preset, error) {
	id = strings.TrimSpace(strings.ToLower(id))
	if id == "" || strings.Contains(id, "/") || strings.Contains(id, `\`) {
		return Preset{}, fmt.Errorf("invalid preset id")
	}
	preset, err := loadPresetFile("presets/" + id + ".json")
	if err != nil {
		return Preset{}, fmt.Errorf("routing preset %q not found", id)
	}
	return preset, nil
}

func loadPresetFile(name string) (Preset, error) {
	data, err := presetFiles.ReadFile(name)
	if err != nil {
		return Preset{}, err
	}
	var preset Preset
	if err := json.Unmarshal(data, &preset); err != nil {
		return Preset{}, fmt.Errorf("parse %s: %w", name, err)
	}
	if strings.TrimSpace(preset.ID) == "" {
		return Preset{}, fmt.Errorf("routing preset %s has empty id", name)
	}
	return preset, nil
}
