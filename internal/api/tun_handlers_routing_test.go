package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"proxyclient/internal/config"
)

// TestComputeRoutingDiff_AddRemove проверяет вычисление diff: +2 правила, -1 правило.
func TestComputeRoutingDiff_AddRemove(t *testing.T) {
	old := &config.RoutingConfig{
		DefaultAction: config.ActionProxy,
		Rules: []config.RoutingRule{
			{Value: "rule1", Action: config.ActionProxy},
			{Value: "rule2", Action: config.ActionDirect},
			{Value: "rule3", Action: config.ActionBlock},
		},
	}
	// Добавляем rule4, rule5; удаляем rule3
	newCfg := &config.RoutingConfig{
		DefaultAction: config.ActionProxy,
		Rules: []config.RoutingRule{
			{Value: "rule1", Action: config.ActionProxy},
			{Value: "rule2", Action: config.ActionDirect},
			{Value: "rule4", Action: config.ActionProxy},
			{Value: "rule5", Action: config.ActionDirect},
		},
	}

	diff := computeRoutingDiff(old, newCfg)

	if diff.RulesAdded != 2 {
		t.Errorf("rules_added=%d, ожидалось 2", diff.RulesAdded)
	}
	if diff.RulesRemoved != 1 {
		t.Errorf("rules_removed=%d, ожидалось 1", diff.RulesRemoved)
	}
	if diff.RulesTotal != 4 {
		t.Errorf("rules_total=%d, ожидалось 4", diff.RulesTotal)
	}
	if diff.DefaultActionChanged {
		t.Error("default_action_changed должен быть false")
	}
}

// TestComputeRoutingDiff_NilOld проверяет что при nil old все правила считаются добавленными.
func TestComputeRoutingDiff_NilOld(t *testing.T) {
	newCfg := &config.RoutingConfig{
		DefaultAction: config.ActionProxy,
		Rules: []config.RoutingRule{
			{Value: "youtube.com", Action: config.ActionProxy},
			{Value: "discord.com", Action: config.ActionProxy},
		},
	}

	diff := computeRoutingDiff(nil, newCfg)

	if diff.RulesAdded != 2 {
		t.Errorf("rules_added=%d, ожидалось 2", diff.RulesAdded)
	}
	if diff.RulesRemoved != 0 {
		t.Errorf("rules_removed=%d, ожидалось 0", diff.RulesRemoved)
	}
	if diff.RulesTotal != 2 {
		t.Errorf("rules_total=%d, ожидалось 2", diff.RulesTotal)
	}
}

// TestComputeRoutingDiff_DefaultActionChanged проверяет что изменение default_action фиксируется.
func TestComputeRoutingDiff_DefaultActionChanged(t *testing.T) {
	old := &config.RoutingConfig{DefaultAction: config.ActionProxy}
	newCfg := &config.RoutingConfig{DefaultAction: config.ActionDirect}

	diff := computeRoutingDiff(old, newCfg)

	if !diff.DefaultActionChanged {
		t.Error("default_action_changed должен быть true")
	}
}

func TestSetupTunRoutes_LastAppliedIsIndependentSnapshot(t *testing.T) {
	_, h, cleanup := buildTunServer(t)
	defer cleanup()

	h.mu.Lock()
	h.routing.Rules = append(h.routing.Rules, config.RoutingRule{
		Value:  "app.exe",
		Type:   config.RuleTypeProcess,
		Action: config.ActionProxy,
	})
	snapshot := cloneRoutingConfig(h.routing)
	lastAppliedLen := len(h.lastApplied.Rules)
	diff := computeRoutingDiff(h.lastApplied, snapshot)
	h.mu.Unlock()

	if lastAppliedLen != 0 {
		t.Fatalf("lastApplied mutated with current routing, len=%d", lastAppliedLen)
	}
	if !diff.ProcessRulesChanged {
		t.Fatal("adding first process rule must require full restart")
	}
}

func TestCloneRoutingConfig_DeepCopiesRulesAndDNS(t *testing.T) {
	src := &config.RoutingConfig{
		DefaultAction: config.ActionDirect,
		BypassEnabled: true,
		Rules: []config.RoutingRule{
			{Value: "example.com", Type: config.RuleTypeDomain, Action: config.ActionProxy},
		},
		DNS: &config.DNSConfig{RemoteDNS: "https://1.1.1.1/dns-query", DirectDNS: "udp://8.8.8.8"},
	}

	got := cloneRoutingConfig(src)
	src.Rules[0].Value = "mutated.example"
	src.DNS.RemoteDNS = "https://9.9.9.9/dns-query"

	if got == src {
		t.Fatal("cloneRoutingConfig returned the original pointer")
	}
	if got.Rules[0].Value != "example.com" {
		t.Fatalf("rules were not deep-copied: %+v", got.Rules)
	}
	if got.DNS == src.DNS || got.DNS.RemoteDNS != "https://1.1.1.1/dns-query" {
		t.Fatalf("DNS was not deep-copied: got=%+v src=%+v", got.DNS, src.DNS)
	}
	if !got.BypassEnabled || got.DefaultAction != config.ActionDirect {
		t.Fatalf("top-level fields not preserved: %+v", got)
	}
}

// TestHandleApply_InvalidConfigReturnsBadRequest проверяет, что handleApply возвращает
// ошибку при невозможности сгенерировать новый sing-box конфиг.
func TestHandleApply_InvalidConfigReturnsBadRequest(t *testing.T) {
	_, h, cleanup := buildTunServer(t)
	defer cleanup()

	// buildTunServer меняет CWD → создаём заглушку ConfigPath в этом CWD.
	// GenerateSingBoxConfig упадёт (пустой SecretKeyPath) и handleApply должен
	// вернуть ошибку, а не молча применить старый конфиг.
	os.WriteFile("config.singbox.json", []byte("{}"), 0644)
	h.xrayConfig.ConfigPath = "config.singbox.json"

	// Устанавливаем lastApplied с 2 правилами
	h.lastApplied = &config.RoutingConfig{
		DefaultAction: config.ActionProxy,
		Rules: []config.RoutingRule{
			{Value: "rule1", Action: config.ActionProxy},
			{Value: "rule3", Action: config.ActionBlock},
		},
	}
	// h.routing: rule1 (сохранён), rule2 (добавлен), rule4 (добавлен), rule3 (удалён) → +2, -1
	h.mu.Lock()
	h.routing = &config.RoutingConfig{
		DefaultAction: config.ActionProxy,
		Rules: []config.RoutingRule{
			{Value: "rule1", Action: config.ActionProxy},
			{Value: "rule2", Action: config.ActionDirect},
			{Value: "rule4", Action: config.ActionBlock},
		},
	}
	h.mu.Unlock()

	req := httptest.NewRequest(http.MethodPost, "/api/tun/apply", nil)
	w := httptest.NewRecorder()
	h.server.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, ожидалось 400", w.Code)
	}

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("JSON parse: %v", err)
	}
	if errMsg, ok := resp["error"]; !ok || !strings.Contains(errMsg, "не удалось сгенерировать конфиг") {
		t.Fatalf("unexpected error response: %q", w.Body.String())
	}
}

func TestTriggerApply_InvalidConfigReturnsError(t *testing.T) {
	_, h, cleanup := buildTunServer(t)
	defer cleanup()

	h.xrayConfig.ConfigPath = "config.singbox.json"

	err := h.TriggerApply()
	if err == nil {
		t.Fatal("expected error when GenerateSingBoxConfig fails")
	}
	if !strings.Contains(err.Error(), "GenerateSingBoxConfig") {
		t.Fatalf("unexpected error: %v", err)
	}

	h.apply.mu.Lock()
	if h.apply.running {
		t.Error("apply.running должен быть false после ошибки")
	}
	h.apply.mu.Unlock()
}
