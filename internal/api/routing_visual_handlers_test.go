package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"proxyclient/internal/config"
	"proxyclient/internal/routing"
)

func TestVisualRoutingGetConvertsCurrentRules(t *testing.T) {
	srv, h, cleanup := buildTunServer(t)
	defer cleanup()
	h.routing = &config.RoutingConfig{
		DefaultAction: config.ActionProxy,
		Rules: []config.RoutingRule{
			{Value: "https://netflix.com/watch", Type: config.RuleTypeDomain, Action: config.ActionProxy},
			{Value: "10.0.0.0/8", Type: config.RuleTypeIP, Action: config.ActionDirect},
			{Value: "geosite:ru", Type: config.RuleTypeGeosite, Action: config.ActionDirect},
		},
	}

	w := getJSON(t, srv.router, "/api/routing/visual")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/routing/visual = %d, body=%s", w.Code, w.Body.String())
	}
	var resp visualRoutingResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Rules) != 3 {
		t.Fatalf("rules len=%d, want 3: %+v", len(resp.Rules), resp.Rules)
	}
	if resp.Rules[0].Match.Type != "domain_suffix" || resp.Rules[0].Match.Values[0] != "netflix.com" {
		t.Fatalf("first visual rule = %+v", resp.Rules[0])
	}
	if resp.Rules[1].Match.Type != "ip_cidr" {
		t.Fatalf("second visual rule type = %q, want ip_cidr", resp.Rules[1].Match.Type)
	}
	if resp.Rules[2].Match.Values[0] != "ru" {
		t.Fatalf("geosite value = %q, want ru", resp.Rules[2].Match.Values[0])
	}
}

func TestVisualRoutingPutPersistsSupportedRules(t *testing.T) {
	srv, h, cleanup := buildTunServer(t)
	defer cleanup()

	req := visualRoutingResponse{
		DefaultAction: config.ActionDirect,
		Rules: []routing.RoutingRule{
			{ID: "stream", Name: "Streaming", Enabled: true, Priority: 2, Action: "proxy", Match: routing.RuleMatch{Type: "domain_suffix", Values: []string{"netflix.com", "youtube.com"}}},
			{ID: "lan", Name: "LAN", Enabled: true, Priority: 1, Action: "direct", Match: routing.RuleMatch{Type: "ip_cidr", Values: []string{"192.168.0.0/16"}}},
			{ID: "off", Name: "Disabled", Enabled: false, Priority: 3, Action: "block", Match: routing.RuleMatch{Type: "domain", Values: []string{"ads.example"}}},
		},
	}
	w := putJSON(t, srv.router, "/api/routing/visual", req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT /api/routing/visual = %d, body=%s", w.Code, w.Body.String())
	}
	if h.routing.DefaultAction != config.ActionDirect {
		t.Fatalf("default action = %q, want direct", h.routing.DefaultAction)
	}
	if len(h.routing.Rules) != 3 {
		t.Fatalf("persisted rules len=%d, want 3: %+v", len(h.routing.Rules), h.routing.Rules)
	}
	if h.routing.Rules[0].Value != "192.168.0.0/16" || h.routing.Rules[0].Action != config.ActionDirect {
		t.Fatalf("first persisted rule = %+v", h.routing.Rules[0])
	}
	if h.routing.Rules[1].Value != "netflix.com" || h.routing.Rules[2].Value != "youtube.com" {
		t.Fatalf("streaming values not expanded in priority order: %+v", h.routing.Rules)
	}
}

func TestVisualRoutingPutRejectsUnsupportedPersistentTypes(t *testing.T) {
	srv, _, cleanup := buildTunServer(t)
	defer cleanup()

	req := visualRoutingResponse{
		DefaultAction: config.ActionProxy,
		Rules: []routing.RoutingRule{
			{ID: "ports", Name: "Ports", Enabled: true, Priority: 1, Action: "block", Match: routing.RuleMatch{Type: "port", Values: []string{"25"}}},
		},
	}
	w := putJSON(t, srv.router, "/api/routing/visual", req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("PUT unsupported type = %d, want 400, body=%s", w.Code, w.Body.String())
	}
}

func TestVisualRoutingTestRule(t *testing.T) {
	srv, _, cleanup := buildTunServer(t)
	defer cleanup()

	req := visualRoutingTestRequest{
		Rule:  routing.RoutingRule{Enabled: true, Action: "proxy", Match: routing.RuleMatch{Type: "domain_suffix", Values: []string{"youtube.com"}}},
		Value: "music.youtube.com",
	}
	w := postJSON(t, srv.router, "/api/routing/visual/test", req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /api/routing/visual/test = %d, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Matches  bool   `json:"matches"`
		Action   string `json:"action"`
		Outbound string `json:"outbound"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Matches || resp.Action != "proxy" || resp.Outbound != "proxy-out" {
		t.Fatalf("test response = %+v", resp)
	}
}

func TestVisualRoutingConflictCheck(t *testing.T) {
	srv, _, cleanup := buildTunServer(t)
	defer cleanup()

	req := map[string]any{"rules": []routing.RoutingRule{
		{ID: "a", Enabled: true, Priority: 1, Action: "proxy", Match: routing.RuleMatch{Type: "domain_suffix", Values: []string{"youtube.com"}}},
		{ID: "b", Enabled: true, Priority: 2, Action: "direct", Match: routing.RuleMatch{Type: "domain", Values: []string{"music.youtube.com"}}},
	}}
	w := postJSON(t, srv.router, "/api/routing/visual/conflicts", req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /api/routing/visual/conflicts = %d, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Conflicts []routing.Conflict `json:"conflicts"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Conflicts) != 1 {
		t.Fatalf("conflicts len=%d, want 1: %+v", len(resp.Conflicts), resp.Conflicts)
	}
}
