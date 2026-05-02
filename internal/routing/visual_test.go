package routing

import (
	"encoding/json"
	"testing"
)

func TestMatchValueTypes(t *testing.T) {
	tests := []struct {
		name  string
		rule  RoutingRule
		value string
	}{
		{"domain", RoutingRule{Enabled: true, Match: RuleMatch{Type: "domain", Values: []string{"netflix.com"}}}, "netflix.com"},
		{"domain_suffix", RoutingRule{Enabled: true, Match: RuleMatch{Type: "domain_suffix", Values: []string{"youtube.com"}}}, "music.youtube.com"},
		{"domain_keyword", RoutingRule{Enabled: true, Match: RuleMatch{Type: "domain_keyword", Values: []string{"bank"}}}, "my-bank.example"},
		{"ip", RoutingRule{Enabled: true, Match: RuleMatch{Type: "ip", Values: []string{"1.1.1.1"}}}, "1.1.1.1"},
		{"ip_cidr", RoutingRule{Enabled: true, Match: RuleMatch{Type: "ip_cidr", Values: []string{"10.0.0.0/8"}}}, "10.1.2.3"},
		{"process", RoutingRule{Enabled: true, Match: RuleMatch{Type: "process", Values: []string{"chrome.exe"}}}, `C:\Program Files\Chrome\chrome.exe`},
		{"geosite", RoutingRule{Enabled: true, Match: RuleMatch{Type: "geosite", Values: []string{"ru"}}}, "geosite:ru"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !MatchValue(tt.rule, tt.value) {
				t.Fatalf("MatchValue(%+v, %q) = false", tt.rule, tt.value)
			}
		})
	}
}

func TestDetectConflicts(t *testing.T) {
	rules := []RoutingRule{
		{ID: "a", Enabled: true, Priority: 1, Action: "proxy", Match: RuleMatch{Type: "domain_suffix", Values: []string{"youtube.com"}}},
		{ID: "b", Enabled: true, Priority: 2, Action: "direct", Match: RuleMatch{Type: "domain", Values: []string{"music.youtube.com"}}},
		{ID: "c", Enabled: true, Priority: 3, Action: "proxy", Match: RuleMatch{Type: "ip_cidr", Values: []string{"10.0.0.0/8"}}},
		{ID: "d", Enabled: true, Priority: 4, Action: "direct", Match: RuleMatch{Type: "ip", Values: []string{"10.1.2.3"}}},
	}
	got := DetectConflicts(rules)
	if len(got) != 2 {
		t.Fatalf("conflicts=%d, want 2: %+v", len(got), got)
	}
}

func TestToSingBoxRulesGolden(t *testing.T) {
	rules := []RoutingRule{
		{ID: "stream", Enabled: true, Priority: 10, Action: "proxy", Match: RuleMatch{Type: "domain_suffix", Values: []string{"Netflix.com", "youtube.com"}}},
		{ID: "lan", Enabled: true, Priority: 20, Action: "direct", Match: RuleMatch{Type: "ip_cidr", Values: []string{"192.168.0.0/16"}}},
		{ID: "app", Enabled: true, Priority: 30, Action: "block", Match: RuleMatch{Type: "process", Values: []string{"bad.exe"}}},
	}
	got, err := json.Marshal(ToSingBoxRules(rules))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `[{"domain_suffix":["netflix.com","youtube.com"],"outbound":"proxy-out"},{"ip_cidr":["192.168.0.0/16"],"outbound":"direct"},{"outbound":"block","process_name":["bad.exe"]}]`
	if string(got) != want {
		t.Fatalf("sing-box rules = %s, want %s", got, want)
	}
}
