package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"proxyclient/internal/config"
	"proxyclient/internal/eventlog"
	"proxyclient/internal/logger"
	"proxyclient/internal/xray"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

func deleteJSON(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, path, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func putJSON(t *testing.T, handler http.Handler, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func getWithQuery(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

// ─── /api/events ──────────────────────────────────────────────────────────────

func TestHandleEvents_NoEventLog_Returns200Empty(t *testing.T) {
	srv := NewServer(Config{
		XRayManager:  &stubXray{},
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
	}, context.Background())
	srv.FinalizeRoutes()

	w := getJSON(t, srv.router, "/api/events")
	if w.Code != http.StatusOK {
		t.Errorf("GET /api/events = %d, want 200", w.Code)
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["events"] == nil {
		t.Error("events field должен присутствовать")
	}
}

func TestHandleEvents_WithEventLog_ReturnsSince(t *testing.T) {
	evLog := eventlog.New(50)
	evLog.Add("test", "info", "сообщение 1")
	evLog.Add("test", "warn", "сообщение 2")

	srv := NewServer(Config{
		XRayManager:  &stubXray{},
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
		EventLog:     evLog,
	}, context.Background())
	srv.FinalizeRoutes()

	// GET без since — все события
	w := getJSON(t, srv.router, "/api/events")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/events = %d", w.Code)
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	events := resp["events"].([]interface{})
	if len(events) < 2 {
		t.Errorf("events len = %d, want >= 2", len(events))
	}
	if resp["latest_id"] == nil {
		t.Error("latest_id должен присутствовать")
	}
}

func TestHandleEvents_SinceParam_FiltersOld(t *testing.T) {
	evLog := eventlog.New(50)
	evLog.Add("test", "info", "старое")
	evLog.Add("test", "info", "новое")

	srv := NewServer(Config{
		XRayManager:  &stubXray{},
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
		EventLog:     evLog,
	}, context.Background())
	srv.FinalizeRoutes()

	w := getWithQuery(t, srv.router, "/api/events?since=999999")
	if w.Code != http.StatusOK {
		t.Errorf("events with high since = %d", w.Code)
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	events := resp["events"].([]interface{})
	if len(events) != 0 {
		t.Errorf("events с since=999999 должен вернуть пустой список, got %d", len(events))
	}
}

func TestHandleEvents_InvalidSince_IgnoresParam(t *testing.T) {
	srv := NewServer(Config{
		XRayManager:  &stubXray{},
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
		EventLog:     eventlog.New(10),
	}, context.Background())
	srv.FinalizeRoutes()

	w := getWithQuery(t, srv.router, "/api/events?since=notanumber")
	if w.Code != http.StatusOK {
		t.Errorf("events с невалидным since = %d, want 200", w.Code)
	}
}

// ─── /api/events/clear ────────────────────────────────────────────────────────

func TestHandleEventsClear_WithEventLog_Clears(t *testing.T) {
	evLog := eventlog.New(50)
	evLog.Add("test", "info", "событие")

	srv := NewServer(Config{
		XRayManager:  &stubXray{},
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
		EventLog:     evLog,
	}, context.Background())
	srv.FinalizeRoutes()

	w := postJSON(t, srv.router, "/api/events/clear", nil)
	if w.Code != http.StatusOK {
		t.Errorf("POST /api/events/clear = %d, want 200", w.Code)
	}
	if events := evLog.GetSince(0); len(events) != 0 {
		t.Fatalf("после Clear буфер должен быть пустым, получено %d событий", len(events))
	}
}

func TestHandleEventsClear_NoEventLog_NoPanic(t *testing.T) {
	srv := NewServer(Config{
		XRayManager:  &stubXray{},
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
	}, context.Background())
	srv.FinalizeRoutes()

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("events/clear без EventLog вызвал panic: %v", r)
		}
	}()

	w := postJSON(t, srv.router, "/api/events/clear", nil)
	if w.Code != http.StatusOK {
		t.Errorf("events/clear без EventLog = %d, want 200", w.Code)
	}
}

// ─── /api/tun/export ──────────────────────────────────────────────────────────

func TestTunExport_Returns200WithJSON(t *testing.T) {
	srv, _, cleanup := buildTunServer(t)
	defer cleanup()

	// Добавляем правило, потом экспортируем
	postJSON(t, srv.router, "/api/tun/rules", map[string]interface{}{
		"value":  "youtube.com",
		"action": "proxy",
	})

	w := getJSON(t, srv.router, "/api/tun/export")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/tun/export = %d (body: %s)", w.Code, w.Body)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	cd := w.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "routing.json") {
		t.Errorf("Content-Disposition = %q, want routing.json filename", cd)
	}

	var cfg config.RoutingConfig
	if err := json.NewDecoder(w.Body).Decode(&cfg); err != nil {
		t.Errorf("export вернул невалидный JSON: %v", err)
	}
	if cfg.DefaultAction == "" {
		t.Error("default_action должен присутствовать в экспорте")
	}
}

func TestTunExport_EmptyRules_ValidJSON(t *testing.T) {
	srv, _, cleanup := buildTunServer(t)
	defer cleanup()

	w := getJSON(t, srv.router, "/api/tun/export")
	if w.Code != http.StatusOK {
		t.Fatalf("export пустых правил = %d", w.Code)
	}
	var cfg config.RoutingConfig
	if err := json.NewDecoder(w.Body).Decode(&cfg); err != nil {
		t.Errorf("пустой экспорт невалидный JSON: %v", err)
	}
}

// ─── /api/tun/import ──────────────────────────────────────────────────────────

func TestTunImport_ValidJSON_Returns200(t *testing.T) {
	srv, _, cleanup := buildTunServer(t)
	defer cleanup()

	payload := config.RoutingConfig{
		DefaultAction: "direct",
		Rules: []config.RoutingRule{
			{Value: "google.com", Type: "domain", Action: "proxy"},
			{Value: "telegram.exe", Type: "process", Action: "direct"},
		},
	}

	w := postJSON(t, srv.router, "/api/tun/import", payload)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /api/tun/import = %d (body: %s)", w.Code, w.Body)
	}

	// Проверяем что правила применились
	wGet := getJSON(t, srv.router, "/api/tun/rules")
	var resp RulesResponse
	if err := json.NewDecoder(wGet.Body).Decode(&resp); err != nil {
		t.Fatalf("decode rules response: %v", err)
	}
	if resp.DefaultAction != "direct" {
		t.Errorf("default_action после import = %q, want direct", resp.DefaultAction)
	}
	if len(resp.Rules) != 2 {
		t.Errorf("rules count после import = %d, want 2", len(resp.Rules))
	}
}

func TestTunImport_InvalidJSON_Returns400(t *testing.T) {
	srv, _, cleanup := buildTunServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/tun/import", strings.NewReader(`{invalid json`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("import невалидного JSON = %d, want 400", w.Code)
	}
}

func TestTunImport_InvalidDefaultAction_Returns400(t *testing.T) {
	srv, _, cleanup := buildTunServer(t)
	defer cleanup()

	payload := map[string]interface{}{
		"default_action": "invalid_action",
		"rules":          []interface{}{},
	}
	w := postJSON(t, srv.router, "/api/tun/import", payload)
	if w.Code != http.StatusBadRequest {
		t.Errorf("import с невалидным default_action = %d, want 400", w.Code)
	}
}

func TestTunImport_EmptyRuleValue_Returns400(t *testing.T) {
	srv, _, cleanup := buildTunServer(t)
	defer cleanup()

	payload := map[string]interface{}{
		"default_action": "proxy",
		"rules": []interface{}{
			map[string]interface{}{"value": "", "action": "direct"},
		},
	}
	w := postJSON(t, srv.router, "/api/tun/import", payload)
	if w.Code != http.StatusBadRequest {
		t.Errorf("import с пустым value = %d, want 400", w.Code)
	}
}

func TestTunImport_NormalizesURLs(t *testing.T) {
	srv, _, cleanup := buildTunServer(t)
	defer cleanup()

	payload := map[string]interface{}{
		"default_action": "proxy",
		"rules": []interface{}{
			map[string]interface{}{"value": "https://youtube.com/watch?v=123", "action": "direct"},
		},
	}
	w := postJSON(t, srv.router, "/api/tun/import", payload)
	if w.Code != http.StatusOK {
		t.Fatalf("import с URL = %d (body: %s)", w.Code, w.Body)
	}

	wGet := getJSON(t, srv.router, "/api/tun/rules")
	var resp RulesResponse
	if err := json.NewDecoder(wGet.Body).Decode(&resp); err != nil {
		t.Fatalf("decode rules response: %v", err)
	}
	if len(resp.Rules) == 0 {
		t.Fatal("после import правил нет")
	}
	// URL должен быть нормализован до "youtube.com"
	if resp.Rules[0].Value != "youtube.com" {
		t.Errorf("value после нормализации = %q, want youtube.com", resp.Rules[0].Value)
	}
}

// ─── /api/tun/apply/status ────────────────────────────────────────────────────

func TestTunApplyStatus_NotRunning_Returns200(t *testing.T) {
	srv, _, cleanup := buildTunServer(t)
	defer cleanup()

	w := getJSON(t, srv.router, "/api/tun/apply/status")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/tun/apply/status = %d", w.Code)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode apply status response: %v", err)
	}

	if resp["running"] == nil {
		t.Error("running поле должно быть в ответе")
	}
	if resp["running"].(bool) {
		t.Error("running должен быть false изначально")
	}
	if resp["last_err"] == nil {
		t.Error("last_err поле должно присутствовать")
	}
	if resp["elapsed_ms"] == nil {
		t.Error("elapsed_ms поле должно присутствовать")
	}
	if resp["estimated_total_ms"] == nil {
		t.Error("estimated_total_ms поле должно присутствовать")
	}
}

// ─── /api/engine/status ───────────────────────────────────────────────────────

func TestEngineStatus_Returns200(t *testing.T) {
	srv := NewServer(Config{
		XRayManager:  &stubXray{},
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
	}, context.Background())
	SetupEngineRoutes(srv)
	srv.FinalizeRoutes()

	w := getJSON(t, srv.router, "/api/engine/status")
	if w.Code != http.StatusOK {
		t.Errorf("GET /api/engine/status = %d, want 200", w.Code)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode engine status response: %v", err)
	}
	if resp["running"] == nil {
		t.Error("running поле должно присутствовать")
	}
	if resp["stage"] == nil {
		t.Error("stage поле должно присутствовать")
	}
}

func TestEngineDownload_AlreadyRunning_Returns409(t *testing.T) {
	srv := NewServer(Config{
		XRayManager:  &stubXray{},
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
	}, context.Background())
	SetupEngineRoutes(srv)
	srv.FinalizeRoutes()

	// Эмулируем "уже запущено"
	globalEngine.mu.Lock()
	globalEngine.running = true
	globalEngine.mu.Unlock()
	defer func() {
		globalEngine.mu.Lock()
		globalEngine.running = false
		globalEngine.mu.Unlock()
	}()

	w := postJSON(t, srv.router, "/api/engine/download", map[string]string{"exec_path": "/tmp/test"})
	if w.Code != http.StatusConflict {
		t.Errorf("double download = %d, want 409", w.Code)
	}
}

func TestEngineDownload_InvalidJSON_Returns400(t *testing.T) {
	srv := NewServer(Config{
		XRayManager:  &stubXray{},
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
	}, context.Background())
	SetupEngineRoutes(srv)
	srv.FinalizeRoutes()

	req := httptest.NewRequest(http.MethodPost, "/api/engine/download", strings.NewReader("{bad json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid JSON download = %d, want 400", w.Code)
	}
}

// ─── /api/status: расширенные сценарии ────────────────────────────────────────

func TestHandleStatus_Restarting_ShowsWarmingTrue(t *testing.T) {
	srv := NewServer(Config{
		XRayManager:  &stubXray{running: false},
		ProxyManager: &stubProxy{enabled: false},
		Logger:       &logger.NoOpLogger{},
	}, context.Background())
	srv.FinalizeRoutes()

	readyAt := time.Now().Add(10 * time.Second).Round(time.Millisecond)
	srv.SetRestarting(readyAt)
	defer srv.ClearRestarting()

	w := getJSON(t, srv.router, "/api/status")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp StatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if !resp.XRay.Warming {
		t.Error("warming должен быть true при restarting=true")
	}
	if resp.XRay.ReadyAt != readyAt.Unix() {
		t.Errorf("ready_at = %d, want %d", resp.XRay.ReadyAt, readyAt.Unix())
	}
	if resp.XRay.ReadyAtMs != readyAt.UnixMilli() {
		t.Errorf("ready_at_ms = %d, want %d", resp.XRay.ReadyAtMs, readyAt.UnixMilli())
	}
}

func TestHandleStatus_NilXRayManager_ShowsWarmingTrue(t *testing.T) {
	srv := NewServer(Config{
		XRayManager:  nil,
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
	}, context.Background())
	srv.FinalizeRoutes()

	w := getJSON(t, srv.router, "/api/status")
	if w.Code != http.StatusOK {
		t.Fatalf("status с nil xray = %d", w.Code)
	}
	var resp StatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if !resp.XRay.Warming {
		t.Error("warming должен быть true когда XRayManager=nil")
	}
}

func TestHandleStatus_ContainsConfigPath(t *testing.T) {
	srv := NewServer(Config{
		XRayManager:  &stubXray{running: true},
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
		ConfigPath:   "/path/to/config.json",
	}, context.Background())
	srv.FinalizeRoutes()

	w := getJSON(t, srv.router, "/api/status")
	var resp StatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if resp.ConfigPath != "/path/to/config.json" {
		t.Errorf("config_path = %q, want /path/to/config.json", resp.ConfigPath)
	}
}

// ─── CORS middleware ──────────────────────────────────────────────────────────

func TestCORS_AllowedOrigin_SetsHeaders(t *testing.T) {
	srv := NewServer(Config{
		XRayManager:  &stubXray{},
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
	}, context.Background())
	srv.FinalizeRoutes()

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Header.Set("Origin", "http://localhost:8080")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Header().Get("Access-Control-Allow-Origin") != "http://localhost:8080" {
		t.Errorf("ACAO header = %q, want http://localhost:8080", w.Header().Get("Access-Control-Allow-Origin"))
	}
	if w.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Error("ACAM header должен быть установлен для разрешённого origin")
	}
}

func TestCORS_ForeignOrigin_Preflight_Returns403(t *testing.T) {
	srv := NewServer(Config{
		XRayManager:  &stubXray{},
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
	}, context.Background())
	srv.FinalizeRoutes()

	req := httptest.NewRequest(http.MethodOptions, "/api/health", nil)
	req.Header.Set("Origin", "http://evil.com")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("preflight от чужого origin = %d, want 403", w.Code)
	}
}

func TestCORS_NoOrigin_PassesThrough(t *testing.T) {
	srv := NewServer(Config{
		XRayManager:  &stubXray{},
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
	}, context.Background())
	srv.FinalizeRoutes()

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	// Нет Origin заголовка — curl/Postman запрос
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("запрос без Origin = %d, want 200", w.Code)
	}
	// Нет CORS заголовков — не нужны
	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("без Origin header не должно быть CORS заголовков")
	}
}

func TestCORS_Options_AllowedOrigin_Returns204(t *testing.T) {
	srv := NewServer(Config{
		XRayManager:  &stubXray{},
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
	}, context.Background())
	srv.FinalizeRoutes()

	req := httptest.NewRequest(http.MethodOptions, "/api/health", nil)
	req.Header.Set("Origin", "app://")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("OPTIONS preflight от разрешённого origin = %d, want 204", w.Code)
	}
}

// ─── /api/geosite ─────────────────────────────────────────────────────────────

func TestGeositeList_Returns200WithItems(t *testing.T) {
	srv := NewServer(Config{
		XRayManager:  &stubXray{},
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
	}, context.Background())
	// Регистрируем geosite маршруты
	api := srv.router.PathPrefix("/api").Subrouter()
	api.HandleFunc("/geosite", srv.handleGeositeList).Methods("GET")
	srv.FinalizeRoutes()

	w := getJSON(t, srv.router, "/api/geosite")
	if w.Code != http.StatusOK {
		t.Errorf("GET /api/geosite = %d, want 200 (body: %s)", w.Code, w.Body)
	}

	var resp GeositeListResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("невалидный JSON от /api/geosite: %v", err)
	}
	if len(resp.Items) == 0 {
		t.Error("items не должен быть пустым — есть предустановленные geosite")
	}
	// Каждый item должен иметь имя
	for _, item := range resp.Items {
		if item.Name == "" {
			t.Error("geosite item без имени")
		}
	}
}

// ─── /api/tun/apply: двойной вызов ставит в очередь (202) ────────────────────

func TestTunApply_DoubleCall_Returns202Queued(t *testing.T) {
	srv, h, cleanup := buildTunServer(t)
	defer cleanup()

	// Вручную выставляем running=true в applyState
	h.apply.mu.Lock()
	h.apply.running = true
	h.apply.mu.Unlock()
	defer func() {
		h.apply.mu.Lock()
		h.apply.running = false
		h.apply.pendingApply = false
		h.apply.mu.Unlock()
	}()

	w := postJSON(t, srv.router, "/api/tun/apply", nil)
	// FIX: теперь вместо 409 возвращается 202 — apply ставится в очередь
	if w.Code != http.StatusAccepted {
		t.Errorf("double apply = %d, want 202 (queued)", w.Code)
	}

	// Проверяем что pendingApply выставлен
	h.apply.mu.Lock()
	pending := h.apply.pendingApply
	h.apply.mu.Unlock()
	if !pending {
		t.Error("pendingApply должен быть true после двойного вызова apply")
	}
}

// ─── /api/tun/rules: проверка корректного DetectRuleType при добавлении ───────

func TestTunAddRule_CIDR_DetectsIPType(t *testing.T) {
	srv, _, cleanup := buildTunServer(t)
	defer cleanup()

	w := postJSON(t, srv.router, "/api/tun/rules", map[string]interface{}{
		"value":  "192.168.1.0/24",
		"action": "direct",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("add CIDR rule = %d (body: %s)", w.Code, w.Body)
	}

	wGet := getJSON(t, srv.router, "/api/tun/rules")
	var resp RulesResponse
	if err := json.NewDecoder(wGet.Body).Decode(&resp); err != nil {
		t.Fatalf("decode rules response: %v", err)
	}
	if len(resp.Rules) == 0 {
		t.Fatal("правило не добавлено")
	}
	if resp.Rules[0].Type != config.RuleTypeIP {
		t.Errorf("тип CIDR правила = %q, want ip", resp.Rules[0].Type)
	}
}

func TestTunAddRule_GeoSiteValue_DetectsGeoType(t *testing.T) {
	srv, _, cleanup := buildTunServer(t)
	defer cleanup()

	w := postJSON(t, srv.router, "/api/tun/rules", map[string]interface{}{
		"value":  "geosite:youtube",
		"action": "proxy",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("add geosite rule = %d (body: %s)", w.Code, w.Body)
	}

	wGet := getJSON(t, srv.router, "/api/tun/rules")
	var resp RulesResponse
	if err := json.NewDecoder(wGet.Body).Decode(&resp); err != nil {
		t.Fatalf("decode rules response: %v", err)
	}
	if len(resp.Rules) == 0 {
		t.Fatal("правило не добавлено")
	}
	if resp.Rules[0].Type != config.RuleTypeGeosite {
		t.Errorf("тип geosite правила = %q, want geosite", resp.Rules[0].Type)
	}
}

func TestTunDeleteRule_CIDR_WithSlash(t *testing.T) {
	srv, _, cleanup := buildTunServer(t)
	defer cleanup()

	// Добавляем CIDR
	postJSON(t, srv.router, "/api/tun/rules", map[string]interface{}{
		"value":  "10.0.0.0/8",
		"action": "direct",
	})

	// Удаляем — путь с '/' должен работать благодаря {value:.+} паттерну
	w := deleteJSON(t, srv.router, "/api/tun/rules/10.0.0.0/8")
	if w.Code != http.StatusOK {
		t.Errorf("DELETE CIDR rule = %d (body: %s), want 200", w.Code, w.Body)
	}

	// Проверяем что удалено
	wGet := getJSON(t, srv.router, "/api/tun/rules")
	var resp RulesResponse
	if err := json.NewDecoder(wGet.Body).Decode(&resp); err != nil {
		t.Fatalf("decode rules response: %v", err)
	}
	for _, r := range resp.Rules {
		if r.Value == "10.0.0.0/8" {
			t.Error("CIDR правило должно быть удалено")
		}
	}
}

// ─── /api/tun/default: неправильный body ─────────────────────────────────────

func TestTunSetDefault_InvalidBody_Returns400(t *testing.T) {
	srv, _, cleanup := buildTunServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/tun/default", strings.NewReader(`{bad json`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("set default с битым JSON = %d, want 400", w.Code)
	}
}

// ─── recovery middleware: паника не роняет сервер ────────────────────────────

func TestRecoveryMiddleware_PanicsRecovered(t *testing.T) {
	srv := NewServer(Config{
		XRayManager:  &stubXray{},
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
	}, context.Background())

	// Добавляем обработчик который паникует
	srv.router.HandleFunc("/api/panic-test", func(w http.ResponseWriter, r *http.Request) {
		panic("тестовая паника")
	})
	srv.FinalizeRoutes()

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("recovery middleware не отработал, паника вышла наружу: %v", r)
		}
	}()

	w := getJSON(t, srv.router, "/api/panic-test")
	// После panic recovery должен вернуть 500
	if w.Code != http.StatusInternalServerError {
		t.Logf("recovery middleware вернул %d (ожидается 500 или другой non-2xx)", w.Code)
	}
}

// ─── XRay uptime через stubXray ───────────────────────────────────────────────

func TestHandleStatus_XRayUptime_FromManager(t *testing.T) {
	srv := NewServer(Config{
		XRayManager:  &stubXray{running: true},
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
	}, context.Background())
	srv.FinalizeRoutes()

	w := getJSON(t, srv.router, "/api/status")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp StatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if !resp.XRay.Running {
		t.Error("xray.running должен быть true")
	}
}

// ─── bulk replace + export round-trip ────────────────────────────────────────

func TestBulkReplaceExportRoundTrip(t *testing.T) {
	srv, _, cleanup := buildTunServer(t)
	defer cleanup()

	// 1. Bulk replace с несколькими правилами
	rules := []config.RoutingRule{
		{Value: "twitch.tv", Type: "domain", Action: "direct"},
		{Value: "youtube.com", Type: "domain", Action: "proxy"},
		{Value: "telegram.exe", Type: "process", Action: "proxy"},
		{Value: "geosite:discord", Type: "geosite", Action: "proxy"},
	}
	wPut := putJSON(t, srv.router, "/api/tun/rules", map[string]interface{}{
		"default_action": "direct",
		"rules":          rules,
	})
	if wPut.Code != http.StatusOK {
		t.Fatalf("bulk replace = %d (body: %s)", wPut.Code, wPut.Body)
	}

	// 2. Export
	wExport := getJSON(t, srv.router, "/api/tun/export")
	if wExport.Code != http.StatusOK {
		t.Fatalf("export = %d", wExport.Code)
	}

	// 3. Парсим экспорт и проверяем round-trip
	var exported config.RoutingConfig
	if err := json.NewDecoder(wExport.Body).Decode(&exported); err != nil {
		t.Fatalf("decode export response: %v", err)
	}

	if exported.DefaultAction != "direct" {
		t.Errorf("exported default_action = %q, want direct", exported.DefaultAction)
	}
	if len(exported.Rules) != 4 {
		t.Errorf("exported rules count = %d, want 4", len(exported.Rules))
	}

	// 4. Импортируем обратно на чистый сервер
	srv2, _, cleanup2 := buildTunServer(t)
	defer cleanup2()

	wImport := postJSON(t, srv2.router, "/api/tun/import", exported)
	if wImport.Code != http.StatusOK {
		t.Fatalf("import = %d (body: %s)", wImport.Code, wImport.Body)
	}

	wGet := getJSON(t, srv2.router, "/api/tun/rules")
	var result RulesResponse
	if err := json.NewDecoder(wGet.Body).Decode(&result); err != nil {
		t.Fatalf("decode rules response: %v", err)
	}
	if len(result.Rules) != 4 {
		t.Errorf("rules после import = %d, want 4", len(result.Rules))
	}
}

func TestAddRoutingRuleConcurrentNoLostUpdate(t *testing.T) {
	srv, h, cleanup := buildTunServer(t)
	defer cleanup()

	const n = 24
	started := make(chan struct{}, n)
	var wg sync.WaitGroup
	errs := make(chan error, n)

	h.mu.Lock()
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			started <- struct{}{}
			errs <- srv.addRoutingRule(config.RoutingRule{
				Value:  fmt.Sprintf("concurrent-%02d.example", i),
				Type:   config.RuleTypeDomain,
				Action: config.ActionProxy,
			})
		}()
	}
	for i := 0; i < n; i++ {
		<-started
	}
	h.mu.Unlock()
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("addRoutingRule returned error: %v", err)
		}
	}

	w := getJSON(t, srv.router, "/api/tun/rules")
	var resp RulesResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode rules: %v", err)
	}
	seen := map[string]bool{}
	for _, rule := range resp.Rules {
		seen[rule.Value] = true
	}
	for i := 0; i < n; i++ {
		value := fmt.Sprintf("concurrent-%02d.example", i)
		if !seen[value] {
			t.Fatalf("lost concurrent routing update for %s; got %d rules: %+v", value, len(resp.Rules), resp.Rules)
		}
	}
}

func TestImportRulesUsesRoutingMutationLock(t *testing.T) {
	srv, _, cleanup := buildTunServer(t)
	defer cleanup()

	body := `{"format":"text","content":"locked-import.example","action":"proxy"}`
	started := make(chan struct{})
	done := make(chan int, 1)

	srv.routingOpMu.Lock()
	go func() {
		close(started)
		req := httptest.NewRequest(http.MethodPost, "/api/tun/rules/import", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.handleImportRules(w, req)
		done <- w.Code
	}()
	<-started

	select {
	case code := <-done:
		srv.routingOpMu.Unlock()
		t.Fatalf("handleImportRules completed with status %d while routingOpMu was held", code)
	case <-time.After(50 * time.Millisecond):
	}

	srv.routingOpMu.Unlock()

	select {
	case code := <-done:
		if code != http.StatusOK {
			t.Fatalf("handleImportRules status = %d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handleImportRules stayed blocked after routingOpMu was released")
	}

	w := getJSON(t, srv.router, "/api/tun/rules")
	var resp RulesResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode rules: %v", err)
	}
	for _, rule := range resp.Rules {
		if rule.Value == "locked-import.example" {
			return
		}
	}
	t.Fatalf("imported rule was not persisted: %+v", resp.Rules)
}

func TestReplaceRoutingAndApplyUsesRoutingMutationLock(t *testing.T) {
	srv, h, cleanup := buildTunServer(t)
	defer cleanup()

	incoming := config.RoutingConfig{
		DefaultAction: config.ActionProxy,
		Rules: []config.RoutingRule{{
			Value:  "bulk-lock.example",
			Type:   config.RuleTypeDomain,
			Action: config.ActionProxy,
		}},
	}
	done := make(chan error, 1)
	started := make(chan struct{})

	srv.routingOpMu.Lock()
	go func() {
		close(started)
		_, _, err := h.replaceRoutingAndApply(incoming)
		done <- err
	}()
	<-started

	select {
	case err := <-done:
		srv.routingOpMu.Unlock()
		t.Fatalf("replaceRoutingAndApply completed while routingOpMu was held: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	srv.routingOpMu.Unlock()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("replaceRoutingAndApply error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("replaceRoutingAndApply stayed blocked after routingOpMu was released")
	}
}

func TestAppProxyRoutesAllowOptionsPreflight(t *testing.T) {
	srv := NewServer(Config{
		XRayManager:  &stubXray{},
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
	}, context.Background())
	srv.SetupAppProxyRoutes(nil, nil, nil)
	srv.FinalizeRoutes()

	req := httptest.NewRequest(http.MethodOptions, "/api/apps/rules", nil)
	req.Header.Set("Origin", "http://localhost:8080")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS /api/apps/rules = %d, want 204; body: %s", w.Code, w.Body)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:8080" {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
	}
}

func TestGeositeRoutesAllowOptionsPreflight(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := NewServer(Config{
		XRayManager:  &stubXray{},
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
	}, ctx)
	srv.SetupFeatureRoutes(ctx)
	srv.FinalizeRoutes()

	req := httptest.NewRequest(http.MethodOptions, "/api/geosite/download", nil)
	req.Header.Set("Origin", "http://localhost:8080")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS /api/geosite/download = %d, want 204; body: %s", w.Code, w.Body)
	}
}

// ─── xray config helpers ──────────────────────────────────────────────────────

func TestXrayConfig_DefaultValues(t *testing.T) {
	cfg := xray.Config{
		ExecutablePath: "./sing-box.exe",
		ConfigPath:     "./config.json",
		SecretKeyPath:  "./secret.key",
	}
	if cfg.ExecutablePath == "" {
		t.Error("ExecutablePath не должен быть пустым")
	}
	if cfg.ConfigPath == "" {
		t.Error("ConfigPath не должен быть пустым")
	}
}
