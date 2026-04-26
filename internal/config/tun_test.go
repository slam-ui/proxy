package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// ─── DetectRuleType ────────────────────────────────────────────────────────

func TestDetectRuleType(t *testing.T) {
	cases := []struct {
		input string
		want  RuleType
	}{
		// Процессы
		{"chrome.exe", RuleTypeProcess},
		{"FIREFOX.EXE", RuleTypeProcess}, // регистр не важен
		{"My App.exe", RuleTypeProcess},

		// Geosite
		{"geosite:youtube", RuleTypeGeosite},
		{"geosite:cn", RuleTypeGeosite},
		{"geosite:category-ads-all", RuleTypeGeosite},

		// IP и CIDR
		{"192.168.1.1", RuleTypeIP},
		{"10.0.0.0/8", RuleTypeIP},
		{"172.16.0.0/12", RuleTypeIP},
		{"2001:db8::1", RuleTypeIP},
		{"::1", RuleTypeIP},

		// Домены (всё остальное)
		{"youtube.com", RuleTypeDomain},
		{"sub.domain.co.uk", RuleTypeDomain},
		{"localhost", RuleTypeDomain},
		{"not-an-ip-10.0.0.x", RuleTypeDomain},
	}

	for _, tc := range cases {
		got := DetectRuleType(tc.input)
		if got != tc.want {
			t.Errorf("DetectRuleType(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ─── LoadRoutingConfig ─────────────────────────────────────────────────────

func TestLoadRoutingConfig_FileNotFound_ReturnsDefault(t *testing.T) {
	cfg, err := LoadRoutingConfig(filepath.Join(t.TempDir(), "nonexistent.json"))
	if err != nil {
		t.Fatalf("ожидали nil error, получили: %v", err)
	}
	if cfg.DefaultAction != ActionProxy {
		t.Errorf("default action = %q, want %q", cfg.DefaultAction, ActionProxy)
	}
	if len(cfg.Rules) != 0 {
		t.Errorf("rules не пустые: %v", cfg.Rules)
	}
}

func TestLoadRoutingConfig_ValidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routing.json")
	content := RoutingConfig{
		DefaultAction: ActionDirect,
		Rules: []RoutingRule{
			{Value: "youtube.com", Type: RuleTypeDomain, Action: ActionProxy},
			{Value: "10.0.0.0/8", Type: RuleTypeIP, Action: ActionDirect},
		},
	}
	data, _ := json.Marshal(content)
	os.WriteFile(path, data, 0644)

	cfg, err := LoadRoutingConfig(path)
	if err != nil {
		t.Fatalf("LoadRoutingConfig failed: %v", err)
	}
	if cfg.DefaultAction != ActionDirect {
		t.Errorf("DefaultAction = %q, want %q", cfg.DefaultAction, ActionDirect)
	}
	if len(cfg.Rules) != 2 {
		t.Errorf("len(Rules) = %d, want 2", len(cfg.Rules))
	}
	if cfg.Rules[0].Value != "youtube.com" {
		t.Errorf("Rules[0].Value = %q", cfg.Rules[0].Value)
	}
}

func TestLoadRoutingConfig_InvalidJSON_ReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(path, []byte("{not valid json"), 0644)

	_, err := LoadRoutingConfig(path)
	if err == nil {
		t.Fatal("ожидали ошибку для невалидного JSON")
	}
}

// ─── SaveRoutingConfig ─────────────────────────────────────────────────────

func TestSaveRoutingConfig_WritesValidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routing.json")
	cfg := &RoutingConfig{
		DefaultAction: ActionBlock,
		Rules: []RoutingRule{
			{Value: "discord.com", Type: RuleTypeDomain, Action: ActionProxy, Note: "gaming"},
		},
	}

	if err := SaveRoutingConfig(path, cfg); err != nil {
		t.Fatalf("SaveRoutingConfig failed: %v", err)
	}

	// Читаем обратно и проверяем
	loaded, err := LoadRoutingConfig(path)
	if err != nil {
		t.Fatalf("LoadRoutingConfig после сохранения: %v", err)
	}
	if loaded.DefaultAction != ActionBlock {
		t.Errorf("DefaultAction = %q, want %q", loaded.DefaultAction, ActionBlock)
	}
	if len(loaded.Rules) != 1 || loaded.Rules[0].Value != "discord.com" {
		t.Errorf("Rules не совпадают: %+v", loaded.Rules)
	}
	if loaded.Rules[0].Note != "gaming" {
		t.Errorf("Note = %q, want %q", loaded.Rules[0].Note, "gaming")
	}
}

func TestSaveRoutingConfig_Atomic_NoTempFileLeft(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routing.json")
	cfg := DefaultRoutingConfig()

	if err := SaveRoutingConfig(path, cfg); err != nil {
		t.Fatalf("SaveRoutingConfig failed: %v", err)
	}

	// Временный файл .tmp должен быть удалён
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("временный файл остался: %s", e.Name())
		}
	}
}

func TestSaveRoutingConfig_InvalidPath_ReturnsError(t *testing.T) {
	err := SaveRoutingConfig("/nonexistent/dir/routing.json", DefaultRoutingConfig())
	if err == nil {
		t.Fatal("ожидали ошибку для недопустимого пути")
	}
}

func TestSaveRoutingConfig_RoundTrip_PreservesOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.json")
	rules := []RoutingRule{
		{Value: "a.com", Type: RuleTypeDomain, Action: ActionProxy},
		{Value: "b.com", Type: RuleTypeDomain, Action: ActionDirect},
		{Value: "c.com", Type: RuleTypeDomain, Action: ActionBlock},
	}
	cfg := &RoutingConfig{DefaultAction: ActionProxy, Rules: rules}
	SaveRoutingConfig(path, cfg)

	loaded, _ := LoadRoutingConfig(path)
	for i, r := range loaded.Rules {
		if r.Value != rules[i].Value || r.Action != rules[i].Action {
			t.Errorf("Rules[%d]: got {%s %s}, want {%s %s}",
				i, r.Value, r.Action, rules[i].Value, rules[i].Action)
		}
	}
}

// ─── DefaultRoutingConfig ──────────────────────────────────────────────────

func TestDefaultRoutingConfig(t *testing.T) {
	cfg := DefaultRoutingConfig()
	if cfg == nil {
		t.Fatal("DefaultRoutingConfig вернул nil")
	}
	if cfg.DefaultAction != ActionProxy {
		t.Errorf("DefaultAction = %q, want proxy", cfg.DefaultAction)
	}
	if cfg.Rules == nil {
		t.Error("Rules = nil, want пустой slice")
	}
}
