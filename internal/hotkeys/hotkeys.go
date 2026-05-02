package hotkeys

import (
	"fmt"
	"sort"
	"strings"
)

type Action string

const (
	ActionToggleConnection Action = "toggle_connection"
	ActionNextServer       Action = "next_server"
	ActionShowHideWindow   Action = "show_hide_window"
)

type Binding struct {
	Action      Action `json:"action"`
	Accelerator string `json:"accelerator"`
	Enabled     bool   `json:"enabled"`
}

type Settings struct {
	Enabled  bool      `json:"enabled"`
	Bindings []Binding `json:"bindings"`
}

type ParsedAccelerator struct {
	Modifiers uint32
	Key       uint32
	Canonical string
}

type Conflict struct {
	Action      Action `json:"action"`
	Accelerator string `json:"accelerator"`
	Error       string `json:"error"`
}

type Registrar interface {
	Register(id int, accelerator ParsedAccelerator) error
	Unregister(id int) error
}

const (
	ModAlt     uint32 = 0x0001
	ModControl uint32 = 0x0002
	ModShift   uint32 = 0x0004
	ModWin     uint32 = 0x0008
)

func DefaultSettings() Settings {
	bindings := []Binding{
		{Action: ActionToggleConnection, Accelerator: "Ctrl+Alt+P", Enabled: true},
		{Action: ActionNextServer, Accelerator: "Ctrl+Alt+S", Enabled: true},
		{Action: ActionShowHideWindow, Accelerator: "Ctrl+Alt+L", Enabled: true},
	}
	for i := 1; i <= 9; i++ {
		bindings = append(bindings, Binding{
			Action:      Action(fmt.Sprintf("profile_%d", i)),
			Accelerator: fmt.Sprintf("Ctrl+Alt+%d", i),
			Enabled:     true,
		})
	}
	return Settings{Enabled: true, Bindings: bindings}
}

func NormalizeSettings(settings Settings) Settings {
	defaults := DefaultSettings()
	if len(settings.Bindings) == 0 {
		return defaults
	}
	known := map[Action]Binding{}
	for _, binding := range defaults.Bindings {
		known[binding.Action] = binding
	}
	out := Settings{Enabled: settings.Enabled, Bindings: make([]Binding, 0, len(defaults.Bindings))}
	seen := map[Action]bool{}
	for _, binding := range settings.Bindings {
		if _, ok := known[binding.Action]; !ok {
			continue
		}
		binding.Accelerator = CanonicalAccelerator(binding.Accelerator)
		out.Bindings = append(out.Bindings, binding)
		seen[binding.Action] = true
	}
	for _, binding := range defaults.Bindings {
		if !seen[binding.Action] {
			out.Bindings = append(out.Bindings, binding)
		}
	}
	sort.SliceStable(out.Bindings, func(i, j int) bool {
		return bindingOrder(out.Bindings[i].Action) < bindingOrder(out.Bindings[j].Action)
	})
	return out
}

func RegisterAll(reg Registrar, settings Settings) []Conflict {
	settings = NormalizeSettings(settings)
	if !settings.Enabled {
		return nil
	}
	var conflicts []Conflict
	for idx, binding := range settings.Bindings {
		if !binding.Enabled || strings.TrimSpace(binding.Accelerator) == "" {
			continue
		}
		accel, err := ParseAccelerator(binding.Accelerator)
		if err != nil {
			conflicts = append(conflicts, Conflict{Action: binding.Action, Accelerator: binding.Accelerator, Error: err.Error()})
			continue
		}
		if err := reg.Register(idx+1, accel); err != nil {
			conflicts = append(conflicts, Conflict{Action: binding.Action, Accelerator: accel.Canonical, Error: err.Error()})
		}
	}
	return conflicts
}

func ParseAccelerator(raw string) (ParsedAccelerator, error) {
	parts := strings.Split(raw, "+")
	var mods uint32
	var key uint32
	seenKey := false
	for _, part := range parts {
		token := strings.ToLower(strings.TrimSpace(part))
		switch token {
		case "":
			continue
		case "ctrl", "control":
			mods |= ModControl
		case "alt":
			mods |= ModAlt
		case "shift":
			mods |= ModShift
		case "win", "windows", "meta":
			mods |= ModWin
		default:
			if seenKey {
				return ParsedAccelerator{}, fmt.Errorf("multiple keys in %q", raw)
			}
			vk, err := parseKey(token)
			if err != nil {
				return ParsedAccelerator{}, err
			}
			key = vk
			seenKey = true
		}
	}
	if !seenKey {
		return ParsedAccelerator{}, fmt.Errorf("missing key in %q", raw)
	}
	if mods == 0 {
		return ParsedAccelerator{}, fmt.Errorf("missing modifier in %q", raw)
	}
	return ParsedAccelerator{Modifiers: mods, Key: key, Canonical: canonical(mods, key)}, nil
}

func CanonicalAccelerator(raw string) string {
	parsed, err := ParseAccelerator(raw)
	if err != nil {
		return strings.TrimSpace(raw)
	}
	return parsed.Canonical
}

func parseKey(token string) (uint32, error) {
	if len(token) == 1 {
		ch := token[0]
		if ch >= 'a' && ch <= 'z' {
			return uint32(ch - 'a' + 'A'), nil
		}
		if ch >= '0' && ch <= '9' {
			return uint32(ch), nil
		}
	}
	switch token {
	case "space":
		return 0x20, nil
	case "escape", "esc":
		return 0x1B, nil
	default:
		return 0, fmt.Errorf("unsupported key %q", token)
	}
}

func canonical(mods, key uint32) string {
	parts := make([]string, 0, 5)
	if mods&ModControl != 0 {
		parts = append(parts, "Ctrl")
	}
	if mods&ModAlt != 0 {
		parts = append(parts, "Alt")
	}
	if mods&ModShift != 0 {
		parts = append(parts, "Shift")
	}
	if mods&ModWin != 0 {
		parts = append(parts, "Win")
	}
	parts = append(parts, keyName(key))
	return strings.Join(parts, "+")
}

func keyName(key uint32) string {
	if key >= 'A' && key <= 'Z' {
		return string(rune(key))
	}
	if key >= '0' && key <= '9' {
		return string(rune(key))
	}
	switch key {
	case 0x20:
		return "Space"
	case 0x1B:
		return "Esc"
	default:
		return fmt.Sprintf("VK_%X", key)
	}
}

func bindingOrder(action Action) int {
	switch action {
	case ActionToggleConnection:
		return 0
	case ActionNextServer:
		return 10
	case ActionShowHideWindow:
		return 20
	default:
		if strings.HasPrefix(string(action), "profile_") {
			return 100 + int(string(action)[len("profile_")]-'0')
		}
		return 1000
	}
}
