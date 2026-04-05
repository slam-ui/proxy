package api

import (
	"proxyclient/internal/config"
	"testing"
)

// TestProcessRulesDetection проверяет что изменение process-правил обнаруживается
func TestProcessRulesChangedDetection(t *testing.T) {
	tests := []struct {
		name               string
		old                *config.RoutingConfig
		new                *config.RoutingConfig
		wantProcessChanged bool
	}{
		{
			name: "first process rule added",
			old: &config.RoutingConfig{
				DefaultAction: config.ActionDirect,
				Rules: []config.RoutingRule{
					{Value: "example.com", Type: config.RuleTypeDomain, Action: config.ActionProxy},
				},
			},
			new: &config.RoutingConfig{
				DefaultAction: config.ActionDirect,
				Rules: []config.RoutingRule{
					{Value: "example.com", Type: config.RuleTypeDomain, Action: config.ActionProxy},
					{Value: "code.exe", Type: config.RuleTypeProcess, Action: config.ActionProxy},
				},
			},
			wantProcessChanged: true,
		},
		{
			name: "process rules removed",
			old: &config.RoutingConfig{
				DefaultAction: config.ActionDirect,
				Rules: []config.RoutingRule{
					{Value: "discord.exe", Type: config.RuleTypeProcess, Action: config.ActionProxy},
				},
			},
			new: &config.RoutingConfig{
				DefaultAction: config.ActionDirect,
				Rules: []config.RoutingRule{
					{Value: "example.com", Type: config.RuleTypeDomain, Action: config.ActionProxy},
				},
			},
			wantProcessChanged: true,
		},
		{
			name: "only domain changes",
			old: &config.RoutingConfig{
				DefaultAction: config.ActionDirect,
				Rules: []config.RoutingRule{
					{Value: "example.com", Type: config.RuleTypeDomain, Action: config.ActionProxy},
				},
			},
			new: &config.RoutingConfig{
				DefaultAction: config.ActionDirect,
				Rules: []config.RoutingRule{
					{Value: "example.com", Type: config.RuleTypeDomain, Action: config.ActionProxy},
					{Value: "another.com", Type: config.RuleTypeDomain, Action: config.ActionProxy},
				},
			},
			wantProcessChanged: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diff := computeRoutingDiff(tt.old, tt.new)
			if diff.ProcessRulesChanged != tt.wantProcessChanged {
				t.Errorf("ProcessRulesChanged = %v, want %v", diff.ProcessRulesChanged, tt.wantProcessChanged)
			}
		})
	}
}
