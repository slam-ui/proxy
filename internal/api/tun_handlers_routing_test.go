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
