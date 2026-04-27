package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"proxyclient/internal/config"
	"proxyclient/internal/logger"
	"proxyclient/internal/proxy"
	"proxyclient/internal/xray"
)

// ─── mock xray.Manager ────────────────────────────────────────────────────

type stubXray struct{ running bool }

func (s *stubXray) Start() error                          { return nil }
func (s *stubXray) Stop() error                           { return nil }
func (s *stubXray) IsRunning() bool                       { return s.running }
func (s *stubXray) GetPID() int                           { return 0 }
func (s *stubXray) Wait() error                           { return nil }
func (s *stubXray) LastOutput() string                    { return "" }
func (s *stubXray) StartAfterManualCleanup() error        { return nil }
func (s *stubXray) Uptime() time.Duration                 { return 0 }
func (s *stubXray) GetHealthStatus() (int, float64, bool) { return 0, 0, false } // БАГ #3
func (s *stubXray) SetHealthAlertFn(fn func())            {}
func (s *stubXray) MemoryMB() uint64                      { return 0 }

// ─── mock proxy.Manager ───────────────────────────────────────────────────

type stubProxy struct{ enabled bool }

func (p *stubProxy) Enable(cfg proxy.Config) error                                { p.enabled = true; return nil }
func (p *stubProxy) Disable() error                                               { p.enabled = false; return nil }
func (p *stubProxy) IsEnabled() bool                                              { return p.enabled }
func (p *stubProxy) GetConfig() proxy.Config                                      { return proxy.Config{} }
func (p *stubProxy) StartGuard(ctx context.Context, interval time.Duration) error { return nil } // B-2
func (p *stubProxy) StopGuard()                                                   {}             // B-2
func (p *stubProxy) PauseGuard(d time.Duration)                                   {}             // БАГ 14
func (p *stubProxy) ResumeGuard()                                                 {}             // БАГ 14

// ─── helpers ──────────────────────────────────────────────────────────────

// buildTunServer создаёт Server + TunHandlers с пустым routing,
// работающим в tmpDir (CWD переключается для изоляции файловых операций).
// Возвращает функцию очистки — вызови её через defer.
func buildTunServer(t *testing.T) (*Server, *TunHandlers, func()) {
	t.Helper()
	dir := t.TempDir()
	// Создаём data/ внутри tmpDir — routingConfigPath = "data/routing.json"
	if err := os.MkdirAll(dir+"/"+config.DataDir, 0755); err != nil {
		t.Fatalf("MkdirAll data/: %v", err)
	}
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	srv := NewServer(Config{
		ListenAddress: ":0",
		XRayManager:   &stubXray{running: true},
		ProxyManager:  &stubProxy{},
		Logger:        &logger.NoOpLogger{},
	}, context.Background())
	// xray.Config нужен TunHandlers только для doApply; в unit-тестах правил не вызываем.
	h := srv.SetupTunRoutes(xray.Config{})
	// Регистрируем все feature-роуты чтобы тесты покрывали реальный набор эндпоинтов
	SetupProfileRoutes(srv)
	SetupSettingsRoutes(srv)
	srv.FinalizeRoutes()

	return srv, h, func() { os.Chdir(old) }
}

func postJSON(t *testing.T, handler http.Handler, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func getJSON(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

// ─── /api/health & /api/status ────────────────────────────────────────────

func TestHandleHealth(t *testing.T) {
	srv := NewServer(Config{
		XRayManager:  &stubXray{},
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
	}, context.Background())
	srv.FinalizeRoutes()

	w := getJSON(t, srv.router, "/api/health")
	if w.Code != http.StatusOK {
		t.Errorf("GET /api/health = %d, want 200", w.Code)
	}
	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "ok" {
		t.Errorf("status = %q, want ok", resp["status"])
	}
}

func TestHandleStatus_XrayRunning(t *testing.T) {
	srv := NewServer(Config{
		XRayManager:  &stubXray{running: true},
		ProxyManager: &stubProxy{enabled: true},
		Logger:       &logger.NoOpLogger{},
	}, context.Background())
	srv.FinalizeRoutes()

	w := getJSON(t, srv.router, "/api/status")
	if w.Code != http.StatusOK {
		t.Errorf("GET /api/status = %d, want 200", w.Code)
	}
	var resp StatusResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if !resp.XRay.Running {
		t.Error("XRay.Running должен быть true")
	}
	if !resp.Proxy.Enabled {
		t.Error("Proxy.Enabled должен быть true")
	}
}

func TestHandleStatus_XrayStopped(t *testing.T) {
	srv := NewServer(Config{
		XRayManager:  &stubXray{running: false},
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
	}, context.Background())
	srv.FinalizeRoutes()

	w := getJSON(t, srv.router, "/api/status")
	var resp StatusResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.XRay.Running {
		t.Error("XRay.Running должен быть false")
	}
}

// ─── /api/tun/rules — GET (list) ──────────────────────────────────────────

func TestTunListRules_EmptyInitially(t *testing.T) {
	srv, _, cleanup := buildTunServer(t)
	defer cleanup()
	srv.FinalizeRoutes()

	w := getJSON(t, srv.router, "/api/tun/rules")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/tun/rules = %d", w.Code)
	}
	var resp RulesResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.Rules) != 0 {
		t.Errorf("Rules = %v, want empty", resp.Rules)
	}
	if resp.DefaultAction != config.ActionProxy {
		t.Errorf("DefaultAction = %q, want proxy", resp.DefaultAction)
	}
}

// ─── /api/tun/rules — POST (add) ──────────────────────────────────────────

func TestTunAddRule_Domain_AutoDetectsType(t *testing.T) {
	srv, _, cleanup := buildTunServer(t)
	defer cleanup()
	srv.FinalizeRoutes()

	w := postJSON(t, srv.router, "/api/tun/rules", AddRuleRequest{
		Value:  "youtube.com",
		Action: config.ActionProxy,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /api/tun/rules = %d, body: %s", w.Code, w.Body)
	}

	// Проверяем что правило появилось
	w2 := getJSON(t, srv.router, "/api/tun/rules")
	var resp RulesResponse
	json.NewDecoder(w2.Body).Decode(&resp)
	if len(resp.Rules) != 1 {
		t.Fatalf("Rules count = %d, want 1", len(resp.Rules))
	}
	if resp.Rules[0].Value != "youtube.com" {
		t.Errorf("Value = %q", resp.Rules[0].Value)
	}
	if resp.Rules[0].Type != config.RuleTypeDomain {
		t.Errorf("Type = %q, want domain", resp.Rules[0].Type)
	}
}

func TestTunAddRule_StripURL_KeepsHost(t *testing.T) {
	srv, _, cleanup := buildTunServer(t)
	defer cleanup()
	srv.FinalizeRoutes()

	w := postJSON(t, srv.router, "/api/tun/rules", AddRuleRequest{
		Value:  "https://youtube.com/watch?v=abc",
		Action: config.ActionProxy,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("POST = %d, body: %s", w.Code, w.Body)
	}
	var result map[string]interface{}
	json.NewDecoder(w.Body).Decode(&result)
	rule := result["rule"].(map[string]interface{})
	if rule["value"] != "youtube.com" {
		t.Errorf("после stripping URL, value = %q, want youtube.com", rule["value"])
	}
}

func TestTunAddRule_EmptyValue_Returns400(t *testing.T) {
	srv, _, cleanup := buildTunServer(t)
	defer cleanup()
	srv.FinalizeRoutes()

	w := postJSON(t, srv.router, "/api/tun/rules", AddRuleRequest{
		Value:  "https://",
		Action: config.ActionProxy,
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("пустое значение должно вернуть 400, got %d", w.Code)
	}
}

func TestTunAddRule_InvalidAction_Returns400(t *testing.T) {
	srv, _, cleanup := buildTunServer(t)
	defer cleanup()
	srv.FinalizeRoutes()

	w := postJSON(t, srv.router, "/api/tun/rules", map[string]string{
		"value":  "site.com",
		"action": "INVALID",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("неверный action должен вернуть 400, got %d", w.Code)
	}
}

func TestTunAddRule_Duplicate_Returns409(t *testing.T) {
	srv, _, cleanup := buildTunServer(t)
	defer cleanup()
	srv.FinalizeRoutes()

	body := AddRuleRequest{Value: "discord.com", Action: config.ActionProxy}
	postJSON(t, srv.router, "/api/tun/rules", body) // первый — OK

	w := postJSON(t, srv.router, "/api/tun/rules", body) // второй — 409
	if w.Code != http.StatusConflict {
		t.Errorf("дубликат должен вернуть 409, got %d", w.Code)
	}
}

func TestTunAddRule_GeoSite_DetectsType(t *testing.T) {
	srv, _, cleanup := buildTunServer(t)
	defer cleanup()
	srv.FinalizeRoutes()

	w := postJSON(t, srv.router, "/api/tun/rules", AddRuleRequest{
		Value:  "geosite:youtube",
		Action: config.ActionProxy,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("POST geosite:youtube = %d", w.Code)
	}
	var result map[string]interface{}
	json.NewDecoder(w.Body).Decode(&result)
	rule := result["rule"].(map[string]interface{})
	if rule["type"] != "geosite" {
		t.Errorf("Type = %q, want geosite", rule["type"])
	}
}

func TestTunAddRule_Process_DetectsType(t *testing.T) {
	srv, _, cleanup := buildTunServer(t)
	defer cleanup()
	srv.FinalizeRoutes()

	w := postJSON(t, srv.router, "/api/tun/rules", AddRuleRequest{
		Value:  "Discord.exe",
		Action: config.ActionProxy,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("POST Discord.exe = %d", w.Code)
	}
	var result map[string]interface{}
	json.NewDecoder(w.Body).Decode(&result)
	rule := result["rule"].(map[string]interface{})
	if rule["type"] != "process" {
		t.Errorf("Type = %q, want process", rule["type"])
	}
}

// ─── /api/tun/rules — DELETE ──────────────────────────────────────────────

func TestTunDeleteRule_ExistingRule_Removes(t *testing.T) {
	srv, _, cleanup := buildTunServer(t)
	defer cleanup()
	srv.FinalizeRoutes()

	postJSON(t, srv.router, "/api/tun/rules", AddRuleRequest{Value: "reddit.com", Action: config.ActionDirect})

	req := httptest.NewRequest(http.MethodDelete, "/api/tun/rules/reddit.com", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE = %d, body: %s", w.Code, w.Body)
	}

	// Подтверждаем удаление
	w2 := getJSON(t, srv.router, "/api/tun/rules")
	var resp RulesResponse
	json.NewDecoder(w2.Body).Decode(&resp)
	if len(resp.Rules) != 0 {
		t.Errorf("после удаления Rules = %v, want empty", resp.Rules)
	}
}

func TestTunDeleteRule_NonExistent_Returns404(t *testing.T) {
	srv, _, cleanup := buildTunServer(t)
	defer cleanup()
	srv.FinalizeRoutes()

	req := httptest.NewRequest(http.MethodDelete, "/api/tun/rules/nosuchsite.com", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("удаление несуществующего правила должно вернуть 404, got %d", w.Code)
	}
}

// ─── /api/tun/rules — PUT (bulk replace) ─────────────────────────────────

func TestTunBulkReplace_ReplacesAll(t *testing.T) {
	srv, _, cleanup := buildTunServer(t)
	defer cleanup()
	srv.FinalizeRoutes()

	// Добавляем начальное правило
	postJSON(t, srv.router, "/api/tun/rules", AddRuleRequest{Value: "old.com", Action: config.ActionProxy})

	newRules := BulkReplaceRequest{
		DefaultAction: config.ActionDirect,
		Rules: []config.RoutingRule{
			{Value: "new1.com", Type: config.RuleTypeDomain, Action: config.ActionProxy},
			{Value: "new2.com", Type: config.RuleTypeDomain, Action: config.ActionBlock},
		},
	}
	data, _ := json.Marshal(newRules)
	req := httptest.NewRequest(http.MethodPut, "/api/tun/rules", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT /api/tun/rules = %d, body: %s", w.Code, w.Body)
	}

	// Проверяем что старое правило исчезло, новые появились
	w2 := getJSON(t, srv.router, "/api/tun/rules")
	var resp RulesResponse
	json.NewDecoder(w2.Body).Decode(&resp)
	if len(resp.Rules) != 2 {
		t.Errorf("Rules count = %d, want 2", len(resp.Rules))
	}
	if resp.DefaultAction != config.ActionDirect {
		t.Errorf("DefaultAction = %q, want direct", resp.DefaultAction)
	}
}

func TestTunBulkReplace_InvalidAction_Returns400(t *testing.T) {
	srv, _, cleanup := buildTunServer(t)
	defer cleanup()
	srv.FinalizeRoutes()

	bad := BulkReplaceRequest{
		Rules: []config.RoutingRule{
			{Value: "x.com", Type: config.RuleTypeDomain, Action: "BAD"},
		},
	}
	data, _ := json.Marshal(bad)
	req := httptest.NewRequest(http.MethodPut, "/api/tun/rules", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("неверный action в bulk replace должен вернуть 400, got %d", w.Code)
	}
}

func TestTunBulkReplace_EmptyValue_Returns400(t *testing.T) {
	srv, _, cleanup := buildTunServer(t)
	defer cleanup()
	srv.FinalizeRoutes()

	bad := BulkReplaceRequest{
		Rules: []config.RoutingRule{
			{Value: "   ", Type: config.RuleTypeDomain, Action: config.ActionProxy},
		},
	}
	data, _ := json.Marshal(bad)
	req := httptest.NewRequest(http.MethodPut, "/api/tun/rules", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("пустое value в bulk replace должно вернуть 400, got %d", w.Code)
	}
}

// ─── /api/tun/default ─────────────────────────────────────────────────────

func TestTunSetDefault_ChangesDefault(t *testing.T) {
	srv, _, cleanup := buildTunServer(t)
	defer cleanup()
	srv.FinalizeRoutes()

	w := postJSON(t, srv.router, "/api/tun/default", map[string]string{"action": "direct"})
	if w.Code != http.StatusOK {
		t.Fatalf("POST /api/tun/default = %d, body: %s", w.Code, w.Body)
	}

	w2 := getJSON(t, srv.router, "/api/tun/rules")
	var resp RulesResponse
	json.NewDecoder(w2.Body).Decode(&resp)
	if resp.DefaultAction != config.ActionDirect {
		t.Errorf("DefaultAction = %q, want direct", resp.DefaultAction)
	}
}

func TestTunSetDefault_InvalidAction_Returns400(t *testing.T) {
	srv, _, cleanup := buildTunServer(t)
	defer cleanup()
	srv.FinalizeRoutes()

	w := postJSON(t, srv.router, "/api/tun/default", map[string]string{"action": "IGNORE"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("неверный default action должен вернуть 400, got %d", w.Code)
	}
}

// ─── Profiles ─────────────────────────────────────────────────────────────

func TestProfiles_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(old)

	srv := NewServer(Config{
		XRayManager:  &stubXray{},
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
	}, context.Background())
	SetupProfileRoutes(srv)
	srv.FinalizeRoutes()

	// Сохраняем профиль
	payload := map[string]interface{}{
		"name": "Test Profile",
		"routing": config.RoutingConfig{
			DefaultAction: config.ActionProxy,
			Rules: []config.RoutingRule{
				{Value: "youtube.com", Type: config.RuleTypeDomain, Action: config.ActionProxy},
			},
		},
	}
	w := postJSON(t, srv.router, "/api/profiles", payload)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /api/profiles = %d, body: %s", w.Code, w.Body)
	}

	// Загружаем
	req := httptest.NewRequest(http.MethodGet, "/api/profiles/Test_Profile", nil)
	w2 := httptest.NewRecorder()
	srv.router.ServeHTTP(w2, req)
	if w2.Code != http.StatusOK {
		t.Fatalf("GET /api/profiles/Test_Profile = %d, body: %s", w2.Code, w2.Body)
	}
}

func TestProfiles_ApplyReplacesTunRules(t *testing.T) {
	srv, _, cleanup := buildTunServer(t)
	defer cleanup()

	// Текущие правила отличаются от профиля.
	postJSON(t, srv.router, "/api/tun/rules", AddRuleRequest{Value: "old.example", Action: config.ActionDirect})

	payload := map[string]interface{}{
		"name": "Work",
		"routing": config.RoutingConfig{
			DefaultAction: config.ActionDirect,
			BypassEnabled: true,
			Rules: []config.RoutingRule{
				{Value: "PROFILE.EXAMPLE", Type: config.RuleTypeDomain, Action: config.ActionProxy},
			},
		},
	}
	w := postJSON(t, srv.router, "/api/profiles", payload)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /api/profiles = %d, body: %s", w.Code, w.Body)
	}

	w = postJSON(t, srv.router, "/api/profiles/Work/apply", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /api/profiles/Work/apply = %d, body: %s", w.Code, w.Body)
	}

	w = getJSON(t, srv.router, "/api/tun/rules")
	var resp RulesResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode rules: %v", err)
	}
	if resp.DefaultAction != config.ActionDirect {
		t.Errorf("DefaultAction = %q, want direct", resp.DefaultAction)
	}
	if !resp.BypassEnabled {
		t.Error("BypassEnabled должен применяться из профиля")
	}
	if len(resp.Rules) != 1 || resp.Rules[0].Value != "profile.example" {
		t.Fatalf("rules после apply = %+v, want one normalized profile.example rule", resp.Rules)
	}
	if resp.Rules[0].Action != config.ActionProxy {
		t.Errorf("rule action = %q, want proxy", resp.Rules[0].Action)
	}
}

func TestProfiles_List(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(old)

	srv := NewServer(Config{
		XRayManager:  &stubXray{},
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
	}, context.Background())
	SetupProfileRoutes(srv)
	srv.FinalizeRoutes()

	// Изначально список пуст
	w := getJSON(t, srv.router, "/api/profiles")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/profiles = %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	profiles := resp["profiles"].([]interface{})
	if len(profiles) != 0 {
		t.Errorf("profiles = %v, want empty", profiles)
	}

	// Добавляем профиль
	postJSON(t, srv.router, "/api/profiles", map[string]interface{}{
		"name":    "Work",
		"routing": config.DefaultRoutingConfig(),
	})

	w2 := getJSON(t, srv.router, "/api/profiles")
	var resp2 map[string]interface{}
	json.NewDecoder(w2.Body).Decode(&resp2)
	profiles2 := resp2["profiles"].([]interface{})
	if len(profiles2) != 1 {
		t.Errorf("после сохранения profiles count = %d, want 1", len(profiles2))
	}
}

func TestProfiles_InvalidName_Returns400(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(old)

	srv := NewServer(Config{
		XRayManager:  &stubXray{},
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
	}, context.Background())
	SetupProfileRoutes(srv)
	srv.FinalizeRoutes()

	cases := []string{
		"",                  // пустое
		"../etc/passwd",     // path traversal
		"name/with/slashes", // слэши
	}
	for _, name := range cases {
		w := postJSON(t, srv.router, "/api/profiles", map[string]interface{}{
			"name":    name,
			"routing": config.DefaultRoutingConfig(),
		})
		if w.Code != http.StatusBadRequest {
			t.Errorf("имя %q должно вернуть 400, got %d", name, w.Code)
		}
	}
}

func TestProfiles_Delete(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(old)

	srv := NewServer(Config{
		XRayManager:  &stubXray{},
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
	}, context.Background())
	SetupProfileRoutes(srv)
	srv.FinalizeRoutes()

	postJSON(t, srv.router, "/api/profiles", map[string]interface{}{
		"name":    "ToDelete",
		"routing": config.DefaultRoutingConfig(),
	})

	req := httptest.NewRequest(http.MethodDelete, "/api/profiles/ToDelete", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE /api/profiles/ToDelete = %d, body: %s", w.Code, w.Body)
	}

	// Убедимся что профиль пропал из списка
	w2 := getJSON(t, srv.router, "/api/profiles")
	var resp map[string]interface{}
	json.NewDecoder(w2.Body).Decode(&resp)
	profiles := resp["profiles"].([]interface{})
	if len(profiles) != 0 {
		t.Errorf("после удаления profiles = %v, want empty", profiles)
	}
}

func TestProfiles_PathTraversal_LoadReturns400(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(old)

	srv := NewServer(Config{
		XRayManager:  &stubXray{},
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
	}, context.Background())
	SetupProfileRoutes(srv)
	srv.FinalizeRoutes()

	req := httptest.NewRequest(http.MethodGet, "/api/profiles/..%2Fpasswd", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Error("path traversal в имени профиля не должен возвращать 200")
	}
}

// ─── /api/settings ────────────────────────────────────────────────────────

func TestGetSettings_ReturnsJSON(t *testing.T) {
	srv, _, cleanup := buildTunServer(t)
	defer cleanup()
	w := getJSON(t, srv.router, "/api/settings")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/settings = %d, want 200", w.Code)
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := resp["autorun"]; !ok {
		t.Error("response missing 'autorun' field")
	}
}

func TestSetAutorun_InvalidBody_Returns400(t *testing.T) {
	srv, _, cleanup := buildTunServer(t)
	defer cleanup()
	req, _ := http.NewRequest(http.MethodPost, "/api/settings/autorun", bytes.NewReader([]byte(`{bad json`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid body = %d, want 400", w.Code)
	}
}

// ─── server.GetXRayManager ────────────────────────────────────────────────

func TestGetXRayManager_ReturnsCurrentManager(t *testing.T) {
	stub := &stubXray{running: true}
	srv := NewServer(Config{
		XRayManager:  stub,
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
	}, context.Background())
	got := srv.GetXRayManager()
	if got != stub {
		t.Error("GetXRayManager should return the configured manager")
	}
}

// ─── handleQuit double-close safety ──────────────────────────────────────

func TestHandleQuit_DoubleCallDoesNotPanic(t *testing.T) {
	quit := make(chan struct{})
	srv := NewServer(Config{
		XRayManager:  &stubXray{},
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
		QuitChan:     quit,
	}, context.Background())
	// Two simultaneous POST /api/quit must not panic
	done := make(chan struct{}, 2)
	for i := 0; i < 2; i++ {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("panic on double quit: %v", r)
				}
				done <- struct{}{}
			}()
			postJSON(t, srv.router, "/api/quit", nil)
		}()
	}
	<-done
	<-done
}

// ─── /api/proxy/enable, /api/proxy/disable, /api/proxy/toggle ─────────────

func newProxySrv(t *testing.T) (*Server, func()) {
	t.Helper()
	srv := NewServer(Config{
		XRayManager:  &stubXray{running: true},
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
	}, context.Background())
	srv.FinalizeRoutes()
	return srv, func() {}
}

func TestProxyEnable_WhenDisabled_Returns200(t *testing.T) {
	srv, cleanup := newProxySrv(t)
	defer cleanup()

	w := postJSON(t, srv.router, "/api/proxy/enable", nil)
	if w.Code != http.StatusOK {
		t.Errorf("POST /api/proxy/enable = %d, want 200 (body: %s)", w.Code, w.Body)
	}
	if !srv.config.ProxyManager.IsEnabled() {
		t.Error("прокси должен быть включён после enable")
	}
}

func TestProxyEnable_WhenAlreadyEnabled_Returns400(t *testing.T) {
	srv, cleanup := newProxySrv(t)
	defer cleanup()

	postJSON(t, srv.router, "/api/proxy/enable", nil)
	w := postJSON(t, srv.router, "/api/proxy/enable", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("double enable = %d, want 400", w.Code)
	}
}

func TestProxyDisable_WhenEnabled_Returns200(t *testing.T) {
	srv, cleanup := newProxySrv(t)
	defer cleanup()

	postJSON(t, srv.router, "/api/proxy/enable", nil)
	w := postJSON(t, srv.router, "/api/proxy/disable", nil)
	if w.Code != http.StatusOK {
		t.Errorf("POST /api/proxy/disable = %d, want 200 (body: %s)", w.Code, w.Body)
	}
	if srv.config.ProxyManager.IsEnabled() {
		t.Error("прокси должен быть выключён после disable")
	}
}

func TestProxyDisable_WhenAlreadyDisabled_Returns400(t *testing.T) {
	srv, cleanup := newProxySrv(t)
	defer cleanup()

	w := postJSON(t, srv.router, "/api/proxy/disable", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("disable when already off = %d, want 400", w.Code)
	}
}

func TestProxyToggle_OffThenOn(t *testing.T) {
	srv, cleanup := newProxySrv(t)
	defer cleanup()

	// Start disabled → toggle ON
	w1 := postJSON(t, srv.router, "/api/proxy/toggle", nil)
	if w1.Code != http.StatusOK {
		t.Errorf("toggle ON = %d, want 200", w1.Code)
	}
	if !srv.config.ProxyManager.IsEnabled() {
		t.Error("после toggle ON прокси должен быть включён")
	}

	// Toggle OFF
	w2 := postJSON(t, srv.router, "/api/proxy/toggle", nil)
	if w2.Code != http.StatusOK {
		t.Errorf("toggle OFF = %d, want 200", w2.Code)
	}
	if srv.config.ProxyManager.IsEnabled() {
		t.Error("после toggle OFF прокси должен быть выключён")
	}
}

func TestProxyToggle_Concurrent_NoRace(t *testing.T) {
	srv, cleanup := newProxySrv(t)
	defer cleanup()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			postJSON(t, srv.router, "/api/proxy/toggle", nil)
		}()
	}
	wg.Wait()
	// Главное: не упал с паникой и IsEnabled возвращает корректное значение
	_ = srv.config.ProxyManager.IsEnabled()
}

// ─── switchClashMode: не паникует если sing-box недоступен ─────────────────

func TestSwitchClashMode_SingBoxDown_NoPanic(t *testing.T) {
	// switchClashMode делает HTTP запрос к Clash API.
	// Если sing-box не запущен — должен молча проглотить ошибку (не фатально).
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("switchClashMode вызвал panic: %v", r)
		}
	}()
	switchClashMode(context.Background(), &logger.NoOpLogger{}, "direct")
	switchClashMode(context.Background(), &logger.NoOpLogger{}, "rule")
}

// A-1: switchClashMode должен завершаться за ≤ 2.5s даже если сервер медлит 3s.
func TestSwitchClashMode_Timeout_CompletesWithin2500ms(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	// Подменяем URL Clash API на наш медленный сервер
	old := clashAPIURL
	clashAPIURL = ts.URL
	defer func() { clashAPIURL = old }()

	start := time.Now()
	switchClashMode(context.Background(), &logger.NoOpLogger{}, "rule")
	elapsed := time.Since(start)

	if elapsed > 2500*time.Millisecond {
		t.Errorf("switchClashMode занял %v, ожидалось ≤ 2.5s (клиент с таймаутом 2s)", elapsed)
	}
}

func TestSwitchClashMode_Non2xxDoesNotLogSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("fail"))
	}))
	defer ts.Close()

	oldURL := clashAPIURL
	clashAPIURL = ts.URL
	defer func() { clashAPIURL = oldURL }()

	var logBuf strings.Builder
	testLogger := &captureLogger{buf: &logBuf}

	switchClashMode(context.Background(), testLogger, "rule")

	logs := logBuf.String()
	if strings.Contains(logs, "TUN режим переключён") {
		t.Fatalf("non-2xx response logged success: %s", logs)
	}
	if !strings.Contains(logs, "500") {
		t.Fatalf("non-2xx response was not logged with status: %s", logs)
	}
}

// A-5: panic в хендлере → в лог записывается строка "Stack:".
func TestRecoveryMiddleware_PanicLogsStack(t *testing.T) {
	var logBuf strings.Builder
	testLogger := &captureLogger{buf: &logBuf}

	srv := NewServer(Config{
		XRayManager:  &stubXray{running: true},
		ProxyManager: &stubProxy{},
		Logger:       testLogger,
	}, context.Background())

	// Регистрируем роут который паникует
	srv.router.HandleFunc("/api/test-panic", func(w http.ResponseWriter, r *http.Request) {
		panic("тест паники")
	}).Methods("GET")
	srv.FinalizeRoutes()

	req := httptest.NewRequest(http.MethodGet, "/api/test-panic", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("panic handler code = %d, want 500", w.Code)
	}
	if !strings.Contains(logBuf.String(), "Stack:") {
		t.Errorf("лог не содержит 'Stack:', got: %q", logBuf.String())
	}
}

// captureLogger захватывает сообщения в строку для тестирования.
type captureLogger struct {
	buf *strings.Builder
	mu  sync.Mutex
}

func (l *captureLogger) Debug(f string, a ...interface{}) { l.write(f, a...) }
func (l *captureLogger) Info(f string, a ...interface{})  { l.write(f, a...) }
func (l *captureLogger) Warn(f string, a ...interface{})  { l.write(f, a...) }
func (l *captureLogger) Error(f string, a ...interface{}) {
	l.write(f, a...)
}

func (l *captureLogger) write(f string, a ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.WriteString(fmt.Sprintf(f, a...))
	l.buf.WriteString("\n")
}

// A-10: 10 быстрых POST запросов → часть получают 429.
func TestRateLimit_MutatingRequests_Returns429(t *testing.T) {
	srv, cleanup := newProxySrv(t)
	defer cleanup()

	got429 := 0
	got200 := 0
	for i := 0; i < 10; i++ {
		w := postJSON(t, srv.router, "/api/proxy/toggle", nil)
		if w.Code == http.StatusTooManyRequests {
			got429++
		} else if w.Code == http.StatusOK {
			got200++
		}
	}

	if got429 == 0 {
		t.Error("ожидалось хотя бы одно 429 при 10 быстрых запросах (burst=5)")
	}
	if got200 == 0 {
		t.Error("ожидался хотя бы один успешный запрос")
	}
}

// A-10: GET запросы не ограничиваются rate limiter.
func TestRateLimit_GetRequestsNotLimited(t *testing.T) {
	srv, cleanup := newProxySrv(t)
	defer cleanup()

	for i := 0; i < 20; i++ {
		w := getJSON(t, srv.router, "/api/status")
		if w.Code == http.StatusTooManyRequests {
			t.Errorf("GET /api/status получил 429 (итерация %d) — GET не должен ограничиваться", i)
		}
	}
}

// ── БАГ 6: handleBackup returns valid ZIP ─────────────────────────────────────

// TestHandleBackup_ValidZIP проверяет что handleBackup возвращает валидный ZIP без двойного Close.
func TestHandleBackup_ValidZIP(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer os.Chdir(old)

	srv, cleanup := newProxySrv(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/backup", nil)
	w := httptest.NewRecorder()
	srv.handleBackup(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", w.Code)
	}
	body := w.Body.Bytes()
	if len(body) == 0 {
		t.Fatal("пустое тело ответа")
	}
	// Проверяем что архив валидный — zip.NewReader не должен вернуть ошибку.
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("невалидный ZIP: %v", err)
	}
	// Должен содержать backup_meta.json
	found := false
	for _, f := range zr.File {
		if f.Name == "backup_meta.json" {
			found = true
			break
		}
	}
	if !found {
		t.Error("backup_meta.json не найден в архиве")
	}
}

// TestHandleBackupRestore_TooLargeFile — БАГ 1.
// Проверяет что файл > 5MB через multipart/form-data отклоняется с 413,
// даже когда Content-Length не задан (MaxBytesReader должен поймать).
func TestHandleBackupRestore_TooLargeFile(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer os.Chdir(old)

	// Пишем 6 MB мусора напрямую в multipart — incompressible, тело реально > 5MB.
	// (6MB нулей сжались бы в ~5KB ZIP, обходя проверку размера.)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "backup.zip")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	// Наполняем 6MB не-нулевыми байтами чтобы исключить сжатие на уровне транспорта.
	garbage := make([]byte, 6*1024*1024)
	for i := range garbage {
		garbage[i] = byte(i & 0xFF)
	}
	if _, err := part.Write(garbage); err != nil {
		t.Fatalf("Write garbage: %v", err)
	}
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/backup/restore", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	// Сбрасываем ContentLength в -1 — проверяем путь через MaxBytesReader,
	// а не ранний выход по Content-Length заголовку.
	req.ContentLength = -1

	srv2, cleanup2 := newProxySrv(t)
	defer cleanup2()

	rw := httptest.NewRecorder()
	srv2.handleBackupRestore(rw, req)

	if rw.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("ожидался 413, получен %d (body: %s)", rw.Code, rw.Body.String())
	}
}
