package api

import (
	"time"
	"context"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"proxyclient/internal/config"
	"proxyclient/internal/logger"
	"proxyclient/internal/proxy"
	"proxyclient/internal/xray"
)

// ─── mock xray.Manager ────────────────────────────────────────────────────

type stubXray struct{ running bool }

func (s *stubXray) Start() error       { return nil }
func (s *stubXray) Stop() error        { return nil }
func (s *stubXray) IsRunning() bool    { return s.running }
func (s *stubXray) GetPID() int        { return 0 }
func (s *stubXray) Wait() error        { return nil }
func (s *stubXray) LastOutput() string              { return "" }
func (s *stubXray) StartAfterManualCleanup() error  { return nil }
func (s *stubXray) Uptime() time.Duration              { return 0 }

// ─── mock proxy.Manager ───────────────────────────────────────────────────

type stubProxy struct{ enabled bool }

func (p *stubProxy) Enable(cfg proxy.Config) error { p.enabled = true; return nil }
func (p *stubProxy) Disable() error                { p.enabled = false; return nil }
func (p *stubProxy) IsEnabled() bool               { return p.enabled }
func (p *stubProxy) GetConfig() proxy.Config       { return proxy.Config{} }

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
