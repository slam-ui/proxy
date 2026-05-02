package routing

import (
	"net"
	"sort"
	"strings"
)

type RoutingRule struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Enabled  bool      `json:"enabled"`
	Priority int       `json:"priority"`
	Match    RuleMatch `json:"match"`
	Action   string    `json:"action"`
	Server   string    `json:"server,omitempty"`
}

type RuleMatch struct {
	Type    string   `json:"type"`
	Values  []string `json:"values"`
	Inverse bool     `json:"inverse,omitempty"`
}

type Conflict struct {
	RuleA  string `json:"rule_a"`
	RuleB  string `json:"rule_b"`
	Value  string `json:"value"`
	Action string `json:"action_a"`
	Other  string `json:"action_b"`
}

func MatchValue(rule RoutingRule, value string) bool {
	if !rule.Enabled {
		return false
	}
	matched := match(rule.Match, strings.TrimSpace(strings.ToLower(value)))
	if rule.Match.Inverse {
		return !matched
	}
	return matched
}

func DetectConflicts(rules []RoutingRule) []Conflict {
	ordered := append([]RoutingRule(nil), rules...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Priority < ordered[j].Priority })
	var conflicts []Conflict
	for i := 0; i < len(ordered); i++ {
		if !ordered[i].Enabled {
			continue
		}
		for j := i + 1; j < len(ordered); j++ {
			if !ordered[j].Enabled || normalizeAction(ordered[i].Action) == normalizeAction(ordered[j].Action) {
				continue
			}
			if value, ok := overlaps(ordered[i].Match, ordered[j].Match); ok {
				conflicts = append(conflicts, Conflict{
					RuleA:  ordered[i].ID,
					RuleB:  ordered[j].ID,
					Value:  value,
					Action: normalizeAction(ordered[i].Action),
					Other:  normalizeAction(ordered[j].Action),
				})
			}
		}
	}
	return conflicts
}

func ToSingBoxRules(rules []RoutingRule) []map[string]any {
	ordered := append([]RoutingRule(nil), rules...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Priority < ordered[j].Priority })
	var out []map[string]any
	for _, rule := range ordered {
		if !rule.Enabled {
			continue
		}
		field := singBoxField(rule.Match.Type)
		if field == "" || len(rule.Match.Values) == 0 {
			continue
		}
		item := map[string]any{
			field:      normalizedValues(rule.Match.Values),
			"outbound": outboundFor(rule),
		}
		if rule.Match.Inverse {
			item["invert"] = true
		}
		out = append(out, item)
	}
	return out
}

func match(m RuleMatch, value string) bool {
	for _, raw := range m.Values {
		candidate := strings.TrimSpace(strings.ToLower(raw))
		if candidate == "" {
			continue
		}
		switch m.Type {
		case "domain":
			if value == candidate {
				return true
			}
		case "domain_suffix":
			if value == candidate || strings.HasSuffix(value, "."+candidate) {
				return true
			}
		case "domain_keyword":
			if strings.Contains(value, candidate) {
				return true
			}
		case "ip":
			if value == candidate {
				return true
			}
		case "ip_cidr":
			ip := net.ParseIP(value)
			_, network, err := net.ParseCIDR(candidate)
			if err == nil && ip != nil && network.Contains(ip) {
				return true
			}
		case "port":
			if value == candidate {
				return true
			}
		case "process":
			if strings.EqualFold(filepathBase(value), filepathBase(candidate)) {
				return true
			}
		case "geosite", "geoip":
			if strings.TrimPrefix(value, m.Type+":") == strings.TrimPrefix(candidate, m.Type+":") {
				return true
			}
		}
	}
	return false
}

func overlaps(a, b RuleMatch) (string, bool) {
	if a.Inverse || b.Inverse {
		return "", false
	}
	for _, av := range normalizedValues(a.Values) {
		for _, bv := range normalizedValues(b.Values) {
			if valuesOverlap(a.Type, av, b.Type, bv) {
				return av, true
			}
		}
	}
	return "", false
}

func valuesOverlap(typeA, a, typeB, b string) bool {
	if typeA == typeB && a == b {
		return true
	}
	if typeA == "domain" && typeB == "domain_suffix" {
		return a == b || strings.HasSuffix(a, "."+b)
	}
	if typeA == "domain_suffix" && typeB == "domain" {
		return b == a || strings.HasSuffix(b, "."+a)
	}
	if typeA == "domain_suffix" && typeB == "domain_suffix" {
		return a == b || strings.HasSuffix(a, "."+b) || strings.HasSuffix(b, "."+a)
	}
	if typeA == "domain_keyword" && strings.Contains(b, a) {
		return true
	}
	if typeB == "domain_keyword" && strings.Contains(a, b) {
		return true
	}
	if typeA == "ip_cidr" && typeB == "ip_cidr" {
		_, na, errA := net.ParseCIDR(a)
		_, nb, errB := net.ParseCIDR(b)
		return errA == nil && errB == nil && (na.Contains(nb.IP) || nb.Contains(na.IP))
	}
	if typeA == "ip" && typeB == "ip_cidr" {
		ip := net.ParseIP(a)
		_, n, err := net.ParseCIDR(b)
		return ip != nil && err == nil && n.Contains(ip)
	}
	if typeA == "ip_cidr" && typeB == "ip" {
		return valuesOverlap(typeB, b, typeA, a)
	}
	return false
}

func singBoxField(t string) string {
	switch t {
	case "domain", "domain_suffix", "domain_keyword", "ip_cidr", "geosite", "geoip":
		return t
	case "ip":
		return "ip_cidr"
	case "process":
		return "process_name"
	case "port":
		return "port"
	default:
		return ""
	}
}

func outboundFor(rule RoutingRule) string {
	switch normalizeAction(rule.Action) {
	case "direct":
		return "direct"
	case "block":
		return "block"
	default:
		if rule.Server != "" {
			return rule.Server
		}
		return "proxy-out"
	}
}

func normalizeAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "direct":
		return "direct"
	case "block":
		return "block"
	default:
		return "proxy"
	}
}

func normalizedValues(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(strings.ToLower(value))
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func filepathBase(value string) string {
	value = strings.ReplaceAll(value, "\\", "/")
	parts := strings.Split(value, "/")
	return parts[len(parts)-1]
}
