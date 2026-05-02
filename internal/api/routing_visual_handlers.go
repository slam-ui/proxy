package api

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	"proxyclient/internal/config"
	"proxyclient/internal/routing"
)

const maxRoutingVisualRequestBytes = 1 << 20

type visualRoutingResponse struct {
	DefaultAction config.RuleAction     `json:"default_action"`
	Rules         []routing.RoutingRule `json:"rules"`
	Conflicts     []routing.Conflict    `json:"conflicts,omitempty"`
}

type visualRoutingTestRequest struct {
	Rule  routing.RoutingRule `json:"rule"`
	Value string              `json:"value"`
}

func SetupRoutingVisualRoutes(s *Server) {
	api := s.router.PathPrefix("/api").Subrouter()
	api.HandleFunc("/routing/visual", s.handleVisualRoutingGet).Methods("GET", "OPTIONS")
	api.HandleFunc("/routing/visual", s.handleVisualRoutingPut).Methods("PUT", "OPTIONS")
	api.HandleFunc("/routing/visual/conflicts", s.handleVisualRoutingConflicts).Methods("GET", "OPTIONS")
	api.HandleFunc("/routing/visual/conflicts", s.handleVisualRoutingCheckConflicts).Methods("POST", "OPTIONS")
	api.HandleFunc("/routing/visual/test", s.handleVisualRoutingTest).Methods("POST", "OPTIONS")
}

func (s *Server) handleVisualRoutingGet(w http.ResponseWriter, _ *http.Request) {
	cfg := s.currentRoutingSnapshot()
	rules := visualRulesFromConfig(cfg)
	s.respondJSON(w, http.StatusOK, visualRoutingResponse{
		DefaultAction: cfg.DefaultAction,
		Rules:         rules,
		Conflicts:     routing.DetectConflicts(rules),
	})
}

func (s *Server) handleVisualRoutingPut(w http.ResponseWriter, r *http.Request) {
	var req visualRoutingResponse
	if !decodeStrictJSON(w, r, &req, maxRoutingVisualRequestBytes) {
		return
	}
	nextRules, err := visualRulesToConfig(req.Rules)
	if err != nil {
		s.respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.DefaultAction == "" {
		req.DefaultAction = config.ActionProxy
	}
	if !config.IsValidRuleAction(req.DefaultAction) {
		s.respondError(w, http.StatusBadRequest, "default_action: proxy | direct | block")
		return
	}
	if err := s.mutateRoutingSnapshot(func(cfg *config.RoutingConfig) (bool, error) {
		cfg.DefaultAction = req.DefaultAction
		cfg.Rules = nextRules
		return true, nil
	}); err != nil {
		s.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.respondJSON(w, http.StatusOK, visualRoutingResponse{
		DefaultAction: req.DefaultAction,
		Rules:         visualRulesFromConfig(s.currentRoutingSnapshot()),
	})
}

func (s *Server) handleVisualRoutingConflicts(w http.ResponseWriter, _ *http.Request) {
	rules := visualRulesFromConfig(s.currentRoutingSnapshot())
	s.respondJSON(w, http.StatusOK, map[string]any{"conflicts": routing.DetectConflicts(rules)})
}

func (s *Server) handleVisualRoutingCheckConflicts(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Rules []routing.RoutingRule `json:"rules"`
	}
	if !decodeStrictJSON(w, r, &req, maxRoutingVisualRequestBytes) {
		return
	}
	s.respondJSON(w, http.StatusOK, map[string]any{"conflicts": routing.DetectConflicts(req.Rules)})
}

func (s *Server) handleVisualRoutingTest(w http.ResponseWriter, r *http.Request) {
	var req visualRoutingTestRequest
	if !decodeStrictJSON(w, r, &req, maxRoutingVisualRequestBytes) {
		return
	}
	matches := routing.MatchValue(req.Rule, req.Value)
	result := map[string]any{
		"matches":  matches,
		"action":   normalizeVisualAction(req.Rule.Action),
		"outbound": visualOutbound(req.Rule),
	}
	s.respondJSON(w, http.StatusOK, result)
}

func visualRulesFromConfig(cfg *config.RoutingConfig) []routing.RoutingRule {
	if cfg == nil {
		return nil
	}
	out := make([]routing.RoutingRule, 0, len(cfg.Rules))
	for i, rule := range cfg.Rules {
		value := config.NormalizeRuleValue(rule.Value)
		if value == "" {
			continue
		}
		matchType := visualMatchType(rule.Type, value)
		out = append(out, routing.RoutingRule{
			ID:       fmt.Sprintf("rule-%03d", i+1),
			Name:     visualRuleName(rule, value),
			Enabled:  true,
			Priority: i + 1,
			Match: routing.RuleMatch{
				Type:   matchType,
				Values: []string{visualRuleValue(matchType, value)},
			},
			Action: string(rule.Action),
		})
	}
	return out
}

func visualRulesToConfig(rules []routing.RoutingRule) ([]config.RoutingRule, error) {
	ordered := append([]routing.RoutingRule(nil), rules...)
	sortVisualRules(ordered)
	out := make([]config.RoutingRule, 0, len(ordered))
	for _, rule := range ordered {
		if !rule.Enabled {
			continue
		}
		if rule.Server != "" {
			return nil, fmt.Errorf("rule %q uses per-server routing, which is not supported by current routing config", visualRuleLabel(rule))
		}
		if rule.Match.Inverse {
			return nil, fmt.Errorf("rule %q uses inverse match, which is not supported by current routing config", visualRuleLabel(rule))
		}
		if !config.IsValidRuleAction(config.RuleAction(normalizeVisualAction(rule.Action))) {
			return nil, fmt.Errorf("rule %q has invalid action", visualRuleLabel(rule))
		}
		ruleType, err := configTypeForVisualMatch(rule.Match.Type)
		if err != nil {
			return nil, fmt.Errorf("rule %q: %w", visualRuleLabel(rule), err)
		}
		for _, raw := range rule.Match.Values {
			value := normalizeVisualRuleValue(rule.Match.Type, raw)
			if value == "" {
				continue
			}
			out = append(out, config.RoutingRule{
				Value:  value,
				Type:   ruleType,
				Action: config.RuleAction(normalizeVisualAction(rule.Action)),
				Note:   visualRuleLabel(rule),
			})
		}
	}
	return out, nil
}

func sortVisualRules(rules []routing.RoutingRule) {
	for i := 1; i < len(rules); i++ {
		for j := i; j > 0 && rules[j].Priority < rules[j-1].Priority; j-- {
			rules[j], rules[j-1] = rules[j-1], rules[j]
		}
	}
}

func visualMatchType(t config.RuleType, value string) string {
	switch t {
	case config.RuleTypeProcess:
		return "process"
	case config.RuleTypeIP:
		if strings.Contains(value, "/") {
			return "ip_cidr"
		}
		return "ip"
	case config.RuleTypeGeosite:
		return "geosite"
	default:
		return "domain_suffix"
	}
}

func visualRuleValue(matchType, value string) string {
	switch matchType {
	case "geosite":
		return strings.TrimPrefix(value, "geosite:")
	default:
		return value
	}
}

func visualRuleName(rule config.RoutingRule, value string) string {
	if note := strings.TrimSpace(rule.Note); note != "" && !strings.HasPrefix(note, temporaryRuleNotePrefix) {
		return note
	}
	return fmt.Sprintf("%s %s", titleVisualAction(rule.Action), value)
}

func configTypeForVisualMatch(matchType string) (config.RuleType, error) {
	switch matchType {
	case "domain", "domain_suffix", "domain_keyword":
		return config.RuleTypeDomain, nil
	case "ip", "ip_cidr":
		return config.RuleTypeIP, nil
	case "process":
		return config.RuleTypeProcess, nil
	case "geosite":
		return config.RuleTypeGeosite, nil
	case "geoip", "port":
		return "", fmt.Errorf("%s persistence requires sing-box route builder support", matchType)
	default:
		return "", fmt.Errorf("unsupported match type %q", matchType)
	}
}

func normalizeVisualRuleValue(matchType, raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	switch matchType {
	case "geosite":
		name := strings.TrimPrefix(strings.ToLower(value), "geosite:")
		if name == "" {
			return ""
		}
		return "geosite:" + name
	case "ip":
		if ip := net.ParseIP(value); ip != nil {
			return ip.String()
		}
		return config.NormalizeRuleValue(value)
	case "ip_cidr":
		if _, network, err := net.ParseCIDR(value); err == nil {
			return network.String()
		}
		return config.NormalizeRuleValue(value)
	case "port":
		if n, err := strconv.Atoi(value); err == nil && n > 0 && n <= 65535 {
			return strconv.Itoa(n)
		}
		return ""
	default:
		return config.NormalizeRuleValue(value)
	}
}

func normalizeVisualAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "direct":
		return string(config.ActionDirect)
	case "block":
		return string(config.ActionBlock)
	default:
		return string(config.ActionProxy)
	}
}

func visualOutbound(rule routing.RoutingRule) string {
	switch normalizeVisualAction(rule.Action) {
	case string(config.ActionDirect):
		return "direct"
	case string(config.ActionBlock):
		return "block"
	default:
		if rule.Server != "" {
			return rule.Server
		}
		return "proxy-out"
	}
}

func visualRuleLabel(rule routing.RoutingRule) string {
	if strings.TrimSpace(rule.Name) != "" {
		return strings.TrimSpace(rule.Name)
	}
	if strings.TrimSpace(rule.ID) != "" {
		return strings.TrimSpace(rule.ID)
	}
	return "unnamed"
}

func titleVisualAction(action config.RuleAction) string {
	switch action {
	case config.ActionDirect:
		return "Direct"
	case config.ActionBlock:
		return "Block"
	default:
		return "Proxy"
	}
}
