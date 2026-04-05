package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"proxyclient/internal/config"
	"proxyclient/internal/logger"
	"proxyclient/internal/proxy"
	"proxyclient/internal/wintun"
	"proxyclient/internal/xray"

	"github.com/gorilla/mux"
)

var routingConfigPath = config.DataDir + "/routing.json"

// tryHotReload перезагружает конфиг sing-box через Clash API без остановки процесса.
// Возвращает nil при успехе, ошибку если API недоступен → вызывающий делает полный перезапуск.
func tryHotReload(configPath string) error {
	absPath, err := filepath.Abs(configPath)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{"path": absPath, "force": true})
	req, err := http.NewRequest(http.MethodPut,
		"http://"+config.ClashAPIAddr+"/configs", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("clash api reload status %d: %s", resp.StatusCode, b)
	}
	return nil
}

// waitForSingBoxReady опрашивает Clash API каждые 200ms пока sing-box не ответит.
// Намного быстрее фиксированного estimatedDone + 35с: sing-box обычно готов за 2–5с.
func waitForSingBoxReady(ctx context.Context, log logger.Logger) error {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(60 * time.Second)
	defer deadline.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("sing-box не ответил за 60 секунд")
		case <-ticker.C:
			resp, err := client.Get("http://" + config.ClashAPIAddr + "/")
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode < 500 {
					log.Info("Sing-box готов (Clash API отвечает)")
					return nil
				}
			}
		}
	}
}

// isValidAction возвращает true если action — одно из допустимых значений.
// BUG FIX #18: валидация action дублировалась в 4 местах (handleAddRule,
// handleBulkReplaceRules, handleSetDefault, handleImport). Вынесена в функцию.
func isValidAction(a config.RuleAction) bool {
	return a == config.ActionProxy || a == config.ActionDirect || a == config.ActionBlock
}

// applyState хранит состояние последнего применения правил
type applyState struct {
	mu              sync.Mutex
	running         bool
	lastErr         string
	validationError string // B-1: ошибка при валидации конфига через sing-box check
	lastPID         int
	startedAt       time.Time // когда начался apply
	estimatedDone   time.Time // оценочное время завершения
	reloadMode      string    // B-11: "hotreload" | "restart" | ""
}

// routingDiff содержит сводку изменений между двумя состояниями routing конфига.
// B-11: вычисляется при каждом handleApply для логирования и ответа клиенту.
type routingDiff struct {
	RulesAdded           int  `json:"rules_added"`
	RulesRemoved         int  `json:"rules_removed"`
	RulesTotal           int  `json:"rules_total"`
	DefaultActionChanged bool `json:"default_action_changed"`
	ProcessRulesChanged  bool `json:"process_rules_changed"` // BUG FIX: при изменении process-правил нужен полный перезапуск
}

// hasProcessRules проверяет есть ли в конфиге process-правила
func hasProcessRules(cfg *config.RoutingConfig) bool {
	if cfg == nil {
		return false
	}
	for _, rule := range cfg.Rules {
		if rule.Type == config.RuleTypeProcess {
			return true
		}
	}
	return false
}

// computeRoutingDiff вычисляет разницу между двумя конфигурациями.
// Если old == nil — все правила в new считаются добавленными.
func computeRoutingDiff(old, newCfg *config.RoutingConfig) routingDiff {
	if old == nil {
		return routingDiff{
			RulesAdded:          len(newCfg.Rules),
			RulesTotal:          len(newCfg.Rules),
			ProcessRulesChanged: hasProcessRules(newCfg),
		}
	}

	oldSet := make(map[string]struct{}, len(old.Rules))
	for _, r := range old.Rules {
		oldSet[r.Value] = struct{}{}
	}
	newSet := make(map[string]struct{}, len(newCfg.Rules))
	for _, r := range newCfg.Rules {
		newSet[r.Value] = struct{}{}
	}

	added := 0
	for v := range newSet {
		if _, ok := oldSet[v]; !ok {
			added++
		}
	}
	removed := 0
	for v := range oldSet {
		if _, ok := newSet[v]; !ok {
			removed++
		}
	}

	// BUG FIX: обнаруживаем изменение в process-правилах.
	// Hot-reload через Clash API не может активировать find_process флаг —
	// нужен полный перезапуск при добавлении/удалении любого process-правила.
	oldHasProcess := hasProcessRules(old)
	newHasProcess := hasProcessRules(newCfg)
	processRulesChanged := oldHasProcess != newHasProcess

	return routingDiff{
		RulesAdded:           added,
		RulesRemoved:         removed,
		RulesTotal:           len(newCfg.Rules),
		DefaultActionChanged: old.DefaultAction != newCfg.DefaultAction,
		ProcessRulesChanged:  processRulesChanged,
	}
}

// TunHandlers обработчики маршрутизации
type TunHandlers struct {
	server       *Server
	xrayConfig   xray.Config
	proxyManager proxy.Manager
	mu           sync.RWMutex
	routing      *config.RoutingConfig
	lastApplied  *config.RoutingConfig // B-11: состояние при последнем apply для diff
	apply        applyState
}

// SetupTunRoutes регистрирует маршруты
func (s *Server) SetupTunRoutes(xrayCfg xray.Config) *TunHandlers {
	routing, err := config.LoadRoutingConfig(routingConfigPath)
	if err != nil {
		s.logger.Warn("Не удалось загрузить routing config: %v", err)
		routing = config.DefaultRoutingConfig()
	}

	h := &TunHandlers{
		server:       s,
		xrayConfig:   xrayCfg,
		proxyManager: s.config.ProxyManager,
		routing:      routing,
	}

	s.router.HandleFunc("/api/tun/rules", h.handleListRules).Methods("GET", "OPTIONS")
	s.router.HandleFunc("/api/tun/rules", h.handleAddRule).Methods("POST", "OPTIONS")
	// PUT /api/tun/rules — атомарная замена всего списка за один запрос.
	// Используется фронтендом вместо N последовательных DELETE+POST, что устраняет
	// зависание WebView2 при большом количестве правил (JS-тред блокировался на 30+ секунд).
	s.router.HandleFunc("/api/tun/rules", h.handleBulkReplaceRules).Methods("PUT", "OPTIONS")
	// BUG FIX #NEW-I: {value:.+} вместо {value} — позволяет удалять CIDR правила с '/'
	// (например 192.168.1.0/24). Без .+ горилла-mux интерпретирует /24 как отдельный
	// сегмент пути и возвращает 404 для DELETE /api/tun/rules/192.168.1.0/24.
	s.router.HandleFunc("/api/tun/rules/{value:.+}", h.handleDeleteRule).Methods("DELETE", "OPTIONS")
	s.router.HandleFunc("/api/tun/default", h.handleSetDefault).Methods("POST", "OPTIONS")
	s.router.HandleFunc("/api/tun/apply", h.handleApply).Methods("POST", "OPTIONS")
	s.router.HandleFunc("/api/tun/apply/status", h.handleApplyStatus).Methods("GET", "OPTIONS")
	s.router.HandleFunc("/api/tun/export", h.handleExport).Methods("GET", "OPTIONS")
	s.router.HandleFunc("/api/tun/import", h.handleImport).Methods("POST", "OPTIONS")
	s.router.HandleFunc("/api/tun/turn", h.handleManualTURN).Methods("POST", "OPTIONS")

	// Сохраняем ссылку чтобы handleConnect мог вызвать TriggerApply при смене сервера.
	s.tunHandlers = h

	return h
}

// handleManualTURN POST /api/tun/turn — ручное включение/выключение TURN туннеля.
// Body: {"enabled": true} или {"enabled": false}
// Когда enabled=true, retryLoop не будет автоматически возвращать на direct-соединение.
func (h *TunHandlers) handleManualTURN(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.server.respondError(w, http.StatusBadRequest, "некорректное тело запроса")
		return
	}

	h.server.configMu.RLock()
	fn := h.server.config.ManualTURNFn
	h.server.configMu.RUnlock()

	if fn == nil {
		h.server.respondError(w, http.StatusServiceUnavailable,
			"мониторинг соединения ещё не запущен — подождите несколько секунд после старта")
		return
	}

	if err := fn(req.Enabled); err != nil {
		h.server.logger.Error("handleManualTURN: ошибка переключения TURN: %v", err)
		h.server.respondError(w, http.StatusInternalServerError, "ошибка переключения: "+err.Error())
		return
	}

	msg := "TURN туннель выключен"
	if req.Enabled {
		msg = "TURN туннель включён вручную"
	}
	h.server.respondJSON(w, http.StatusOK, MessageResponse{Message: msg, Success: true})
}

// TriggerApply запускает перегенерацию конфига и перезапуск sing-box без HTTP-контекста.
// Вызывается из handleConnect после обновления secret.key.
// Возвращает ошибку если применение уже запущено или идёт crash-recovery (не блокирует).
func (h *TunHandlers) TriggerApply() error {
	if h.server.IsRestarting() {
		return fmt.Errorf("sing-box восстанавливается после сбоя TUN — повторите позже")
	}
	h.apply.mu.Lock()
	if h.apply.running {
		h.apply.mu.Unlock()
		return fmt.Errorf("применение уже выполняется")
	}
	h.apply.running = true
	h.apply.lastErr = ""
	h.apply.startedAt = time.Now()
	h.apply.estimatedDone = time.Now().Add(5 * time.Second)
	h.apply.mu.Unlock()

	h.mu.RLock()
	snapshot := &config.RoutingConfig{
		DefaultAction: h.routing.DefaultAction,
		Rules:         make([]config.RoutingRule, len(h.routing.Rules)),
	}
	copy(snapshot.Rules, h.routing.Rules)
	h.mu.RUnlock()

	tmpConfigPath := h.xrayConfig.ConfigPath + ".pending"
	if err := config.GenerateSingBoxConfig(h.xrayConfig.SecretKeyPath, tmpConfigPath, snapshot); err != nil {
		_ = os.Remove(tmpConfigPath)
		h.apply.mu.Lock()
		h.apply.running = false
		h.apply.lastErr = err.Error()
		h.apply.mu.Unlock()
		return fmt.Errorf("GenerateSingBoxConfig: %w", err)
	}

	go h.doApply(snapshot, tmpConfigPath)
	return nil
}

// TriggerApplyWithConfig запускает перезапуск sing-box с уже готовым конфигом на диске.
// В отличие от TriggerApply, НЕ перегенерирует конфиг — использует тот что уже лежит
// по configPath. Предназначен для applyTURNMode: конфиг уже записан с TURN override,
// перегенерация через GenerateSingBoxConfig уничтожила бы его (bug: TURN не работал).
func (h *TunHandlers) TriggerApplyWithConfig() error {
	h.apply.mu.Lock()
	if h.apply.running {
		h.apply.mu.Unlock()
		return fmt.Errorf("применение уже выполняется")
	}
	h.apply.running = true
	h.apply.lastErr = ""
	h.apply.startedAt = time.Now()
	h.apply.estimatedDone = time.Now().Add(5 * time.Second)
	h.apply.mu.Unlock()

	h.mu.RLock()
	snapshot := &config.RoutingConfig{
		DefaultAction: h.routing.DefaultAction,
		Rules:         make([]config.RoutingRule, len(h.routing.Rules)),
	}
	copy(snapshot.Rules, h.routing.Rules)
	h.mu.RUnlock()

	// Используем существующий конфиг (уже записан вызывающей стороной с TURN override).
	// tmpConfigPath="" означает для doApply: не переименовывать, применять текущий config.
	go h.doApply(snapshot, "")
	return nil
}

// RulesResponse ответ GET /api/tun/rules
type RulesResponse struct {
	DefaultAction config.RuleAction    `json:"default_action"`
	Rules         []config.RoutingRule `json:"rules"`
}

// handleListRules GET /api/tun/rules
func (h *TunHandlers) handleListRules(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	resp := RulesResponse{
		DefaultAction: h.routing.DefaultAction,
		Rules:         h.routing.Rules,
	}
	if resp.Rules == nil {
		resp.Rules = []config.RoutingRule{}
	}
	h.server.respondJSON(w, http.StatusOK, resp)
}

// AddRuleRequest тело POST /api/tun/rules
type AddRuleRequest struct {
	Value  string            `json:"value"`
	Action config.RuleAction `json:"action"`
	Note   string            `json:"note"`
}

// handleAddRule POST /api/tun/rules
func (h *TunHandlers) handleAddRule(w http.ResponseWriter, r *http.Request) {
	var req AddRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.server.respondError(w, http.StatusBadRequest, "некорректное тело запроса")
		return
	}

	val := config.NormalizeRuleValue(req.Value)

	// BUG FIX: пустой val после стриппинга URL (например "https://") приводил к добавлению
	// правила с пустым значением, что генерировало некорректный sing-box конфиг.
	if strings.TrimSpace(val) == "" {
		h.server.respondError(w, http.StatusBadRequest, "пустое значение правила")
		return
	}

	ruleType := config.DetectRuleType(strings.ToLower(val))

	// FIX: process rules сохраняем в оригинальном регистре.
	// sing-box матчит process_name с учётом регистра на Windows — "telegram.exe" не совпадёт
	// с реальным процессом "Telegram.exe". Домены и IP приводим к lowercase, .exe — нет.
	if ruleType != config.RuleTypeProcess {
		val = strings.ToLower(val)
	}

	if !isValidAction(req.Action) {
		h.server.respondError(w, http.StatusBadRequest, "action: proxy | direct | block")
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	for _, rule := range h.routing.Rules {
		if rule.Value == val {
			h.server.respondError(w, http.StatusConflict, "правило уже существует")
			return
		}
	}

	newRule := config.RoutingRule{
		Value:  val,
		Type:   ruleType,
		Action: req.Action,
		Note:   req.Note,
	}
	h.routing.Rules = append(h.routing.Rules, newRule)

	// BUG FIX: при ошибке сохранения откатываем in-memory изменение и возвращаем ошибку клиенту.
	// Ранее возвращался 201 Created даже если файл не был записан — на следующем рестарте
	// правило исчезало без каких-либо предупреждений (тихая потеря данных).
	if err := config.SaveRoutingConfig(routingConfigPath, h.routing); err != nil {
		h.routing.Rules = h.routing.Rules[:len(h.routing.Rules)-1] // откат
		h.server.logger.Error("Не удалось сохранить routing config: %v", err)
		h.server.respondError(w, http.StatusInternalServerError, "не удалось сохранить правило")
		return
	}

	h.server.respondJSON(w, http.StatusCreated, map[string]interface{}{
		"message": "правило добавлено",
		"rule":    newRule,
	})
}

// BulkReplaceRequest тело PUT /api/tun/rules
type BulkReplaceRequest struct {
	DefaultAction config.RuleAction    `json:"default_action"`
	Rules         []config.RoutingRule `json:"rules"`
}

// handleBulkReplaceRules PUT /api/tun/rules — атомарно заменяет весь список правил.
// Вызывается фронтендом вместо N последовательных DELETE+POST: один HTTP-запрос
// вместо 50+, что предотвращает зависание WebView2.
func (h *TunHandlers) handleBulkReplaceRules(w http.ResponseWriter, r *http.Request) {
	var req BulkReplaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.server.respondError(w, http.StatusBadRequest, "некорректное тело запроса")
		return
	}

	// Валидируем и нормализуем каждое правило.
	// BUG FIX: handleAddRule вызывает NormalizeRuleValue + DetectRuleType,
	// bulk replace не вызывал — правила сохранялись с URL-префиксами и
	// неверными типами, sing-box их не матчил.
	// BUG FIX #NEW-1: добавлено приведение к lowercase для non-process правил —
	// аналогично handleAddRule и handleImport. Без этого drag-and-drop реорганизация
	// правил через PUT /api/tun/rules сохраняла uppercase домены которые sing-box
	// не матчил (domain matching регистрозависимый в sing-box).
	for i, rule := range req.Rules {
		val := config.NormalizeRuleValue(rule.Value)
		if val == "" {
			h.server.respondError(w, http.StatusBadRequest,
				fmt.Sprintf("правило #%d: пустое значение после нормализации", i+1))
			return
		}
		if !isValidAction(rule.Action) {
			h.server.respondError(w, http.StatusBadRequest,
				fmt.Sprintf("правило #%d: неверный action (proxy|direct|block)", i+1))
			return
		}
		ruleType := config.DetectRuleType(strings.ToLower(val))
		// FIX: process rules сохраняем в оригинальном регистре (Windows process_name чувствителен).
		// Домены и IP приводим к lowercase.
		if ruleType != config.RuleTypeProcess {
			val = strings.ToLower(val)
		}
		req.Rules[i].Value = val
		req.Rules[i].Type = ruleType
	}

	// Валидируем default_action (пустая строка → оставляем текущий)
	if req.DefaultAction != "" && !isValidAction(req.DefaultAction) {
		h.server.respondError(w, http.StatusBadRequest, "default_action: proxy | direct | block")
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	oldRules := h.routing.Rules
	oldDefault := h.routing.DefaultAction

	h.routing.Rules = req.Rules
	if req.DefaultAction != "" {
		h.routing.DefaultAction = req.DefaultAction
	}

	if err := config.SaveRoutingConfig(routingConfigPath, h.routing); err != nil {
		// Откат
		h.routing.Rules = oldRules
		h.routing.DefaultAction = oldDefault
		h.server.logger.Error("Bulk replace: не удалось сохранить routing config: %v", err)
		h.server.respondError(w, http.StatusInternalServerError, "не удалось сохранить правила")
		return
	}

	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"message": "правила обновлены",
		"count":   len(req.Rules),
	})
}

// handleDeleteRule DELETE /api/tun/rules/{value}
func (h *TunHandlers) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	value := strings.ToLower(vars["value"])

	h.mu.Lock()
	defer h.mu.Unlock()

	newRules := make([]config.RoutingRule, 0, len(h.routing.Rules))
	found := false
	for _, rule := range h.routing.Rules {
		if strings.ToLower(rule.Value) == value {
			found = true
			continue
		}
		newRules = append(newRules, rule)
	}

	if !found {
		h.server.respondError(w, http.StatusNotFound, "правило не найдено")
		return
	}

	oldRules := h.routing.Rules // сохраняем для отката
	h.routing.Rules = newRules

	// BUG FIX: при ошибке сохранения откатываем удаление и возвращаем ошибку клиенту.
	if err := config.SaveRoutingConfig(routingConfigPath, h.routing); err != nil {
		h.routing.Rules = oldRules // откат
		h.server.logger.Error("Не удалось сохранить routing config: %v", err)
		h.server.respondError(w, http.StatusInternalServerError, "не удалось сохранить изменения")
		return
	}

	h.server.respondJSON(w, http.StatusOK, MessageResponse{Message: "правило удалено", Success: true})
}

// handleSetDefault POST /api/tun/default
func (h *TunHandlers) handleSetDefault(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action config.RuleAction `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.server.respondError(w, http.StatusBadRequest, "некорректное тело запроса")
		return
	}
	if !isValidAction(req.Action) {
		h.server.respondError(w, http.StatusBadRequest, "action: proxy | direct | block")
		return
	}

	h.mu.Lock()
	oldAction := h.routing.DefaultAction
	h.routing.DefaultAction = req.Action

	// BUG FIX: при ошибке сохранения откатываем изменение и возвращаем ошибку клиенту.
	if err := config.SaveRoutingConfig(routingConfigPath, h.routing); err != nil {
		h.routing.DefaultAction = oldAction // откат
		h.mu.Unlock()
		h.server.logger.Error("Не удалось сохранить routing config: %v", err)
		h.server.respondError(w, http.StatusInternalServerError, "не удалось сохранить изменения")
		return
	}
	h.mu.Unlock()

	h.server.respondJSON(w, http.StatusOK, MessageResponse{Message: "дефолтное действие обновлено", Success: true})
}

// handleApply POST /api/tun/apply — запускает перезапуск асинхронно и сразу возвращает ответ
func (h *TunHandlers) handleApply(w http.ResponseWriter, r *http.Request) {
	// Блокируем apply пока handleCrash выполняет TUN recovery:
	// оба пути вызывают wintun.RemoveStaleTunAdapter + PollUntilFree + запуск sing-box,
	// параллельный запуск даёт двойной sing-box → повторный TUN conflict.
	if h.server.IsRestarting() {
		h.server.respondError(w, http.StatusConflict,
			"sing-box восстанавливается после сбоя TUN — дождитесь завершения и повторите")
		return
	}

	h.apply.mu.Lock()
	if h.apply.running {
		h.apply.mu.Unlock()
		h.server.respondError(w, http.StatusConflict, "применение уже выполняется")
		return
	}
	h.apply.running = true
	h.apply.lastErr = ""
	h.apply.startedAt = time.Now()
	h.apply.estimatedDone = time.Now().Add(5 * time.Second) // минимальный буфер; готовность через Clash API probe
	h.apply.mu.Unlock()

	h.mu.RLock()
	snapshot := &config.RoutingConfig{
		DefaultAction: h.routing.DefaultAction,
		Rules:         make([]config.RoutingRule, len(h.routing.Rules)),
	}
	copy(snapshot.Rules, h.routing.Rules)
	h.mu.RUnlock()

	// FIX: pre-validate конфиг до запуска горутины.
	// Аналог nekoray BuildConfig — строит конфиг в памяти и возвращает ошибку
	// ДО любых деструктивных действий. Если новый конфиг не генерируется,
	// apply должен завершиться с ошибкой, а не молча оставить старый конфиг.
	tmpConfigPath := h.xrayConfig.ConfigPath + ".pending"
	if err := config.GenerateSingBoxConfig(h.xrayConfig.SecretKeyPath, tmpConfigPath, snapshot); err != nil {
		_ = os.Remove(tmpConfigPath)
		h.apply.mu.Lock()
		h.apply.running = false
		h.apply.lastErr = err.Error()
		h.apply.validationError = err.Error()
		h.apply.mu.Unlock()
		h.server.logger.Error("Не удалось сгенерировать конфиг sing-box: %v", err)
		h.server.respondError(w, http.StatusBadRequest, "не удалось сгенерировать конфиг: "+err.Error())
		return
	}

	// B-11: вычисляем diff синхронно до запуска горутины
	h.mu.RLock()
	diff := computeRoutingDiff(h.lastApplied, snapshot)
	h.mu.RUnlock()

	// B-11: логируем diff при каждом apply
	defaultChange := ""
	if diff.DefaultActionChanged && h.lastApplied != nil {
		defaultChange = fmt.Sprintf(", default_action %s→%s", h.lastApplied.DefaultAction, snapshot.DefaultAction)
	}
	processChange := ""
	if diff.ProcessRulesChanged {
		oldHas := hasProcessRules(h.lastApplied)
		newHas := hasProcessRules(snapshot)
		processChange = fmt.Sprintf(", process-правила: %v→%v ⚠️ ТРЕБУЕТСЯ ПОЛНЫЙ ПЕРЕЗАПУСК", oldHas, newHas)
	}
	h.server.logger.Info("B-11: apply запущен: +%d правила, -%d, итого %d%s%s",
		diff.RulesAdded, diff.RulesRemoved, diff.RulesTotal, defaultChange, processChange)

	h.server.respondJSON(w, http.StatusAccepted, map[string]interface{}{
		"message":                "применение запущено",
		"rules":                  len(snapshot.Rules),
		"rules_added":            diff.RulesAdded,           // B-11
		"rules_removed":          diff.RulesRemoved,         // B-11
		"rules_total":            diff.RulesTotal,           // B-11
		"default_action_changed": diff.DefaultActionChanged, // B-11
	})

	go h.doApply(snapshot, tmpConfigPath)
}

// doApply выполняет перезапуск sing-box в фоновой горутине.
// tmpConfigPath — путь к предварительно сгенерированному конфигу (или "" если использовать существующий).
func (h *TunHandlers) doApply(snapshot *config.RoutingConfig, tmpConfigPath string) {
	setErr := func(err string) {
		h.apply.mu.Lock()
		h.apply.lastErr = err
		h.apply.mu.Unlock()
	}
	setValidationErr := func(err string) {
		h.apply.mu.Lock()
		h.apply.validationError = err
		h.apply.mu.Unlock()
	}

	defer func() {
		h.apply.mu.Lock()
		h.apply.running = false
		h.apply.mu.Unlock()
	}()

	// Hot reload: если sing-box уже запущен, пробуем перезагрузить конфиг без перезапуска.
	// Это позволяет избежать PollUntilFree (60–180с) при изменении routing rules.
	// BUG FIX: НО если процесс-правила изменились (добавились/удалились),
	// hot-reload через Clash API не может активировать find_process флаг.
	// В этом случае нужен ПОЛНЫЙ ПЕРЕЗАПУСК sing-box!
	{
		h.server.configMu.RLock()
		hotMgr := h.server.config.XRayManager
		h.server.configMu.RUnlock()

		diff := computeRoutingDiff(h.lastApplied, snapshot)
		skipHotReload := diff.ProcessRulesChanged
		if skipHotReload {
			h.server.logger.Info("Process-правила изменились (старые: %v, новые: %v) — пропускаем hot-reload",
				hasProcessRules(h.lastApplied), hasProcessRules(snapshot))
		}

		if tmpConfigPath != "" && hotMgr != nil && hotMgr.IsRunning() && !skipHotReload {
			if err := tryHotReload(tmpConfigPath); err == nil {
				h.server.logger.Info("Hot reload конфига успешен, перезапуск не нужен")
				finalPath := h.xrayConfig.ConfigPath
				if renameErr := os.Rename(tmpConfigPath, finalPath); renameErr != nil {
					h.server.logger.Warn("Hot reload: не удалось переименовать конфиг: %v", renameErr)
				}
				h.mu.Lock()
				// B-11: вычисляем и логируем diff, обновляем lastApplied
				diff := computeRoutingDiff(h.lastApplied, snapshot)
				h.server.logger.Info("B-11: apply завершён: +%d, -%d, итого %d, reload_mode=hotreload",
					diff.RulesAdded, diff.RulesRemoved, diff.RulesTotal)
				h.lastApplied = snapshot
				h.routing = snapshot
				h.mu.Unlock()
				h.apply.mu.Lock()
				h.apply.reloadMode = "hotreload" // B-11
				h.apply.mu.Unlock()
				h.server.ClearRestarting()
				return
			} else {
				h.server.logger.Info("Hot reload недоступен (%v), выполняем полный перезапуск", err)
			}
		}
	}

	// B-1: Валидируем новый конфиг ДО остановки текущего процесса.
	// Если валидация провалена — не трогаем работающий sing-box, удаляем .pending.
	if tmpConfigPath != "" && h.xrayConfig.ExecutablePath != "" {
		if err := xray.ValidateSingBoxConfig(h.server.lifecycleCtx, h.xrayConfig.ExecutablePath, tmpConfigPath); err != nil {
			h.server.logger.Error("Валидация конфига провалена: %v", err)
			setValidationErr(err.Error())
			setErr("конфиг невалиден, текущий процесс остался без изменений")
			_ = os.Remove(tmpConfigPath)
			return
		}
		setValidationErr("") // очищаем старую ошибку валидации
	}

	// Запоминаем состояние прокси и отключаем его на время рестарта,
	// чтобы трафик не уходил в недоступный sing-box.
	proxyWasEnabled := h.proxyManager.IsEnabled()
	proxyConfig := h.proxyManager.GetConfig()
	if proxyWasEnabled {
		if err := h.proxyManager.Disable(); err != nil {
			h.server.logger.Warn("Не удалось отключить прокси перед рестартом: %v", err)
		}
	}

	// Восстанавливаем прокси при выходе (только если sing-box успешно поднялся).
	// Флаг успеха выставляется явно в конце функции.
	restoreProxy := false
	defer func() {
		if restoreProxy && proxyWasEnabled {
			if err := h.proxyManager.Enable(proxyConfig); err != nil {
				h.server.logger.Error("Не удалось восстановить прокси после рестарта: %v", err)
			}
		}
	}()

	// Останавливаем текущий процесс.
	h.server.configMu.RLock()
	currentManager := h.server.config.XRayManager
	h.server.configMu.RUnlock()
	if currentManager != nil {
		if err := currentManager.Stop(); err != nil {
			h.server.logger.Warn("Ошибка при остановке: %v", err)
		}
	}

	// BUG FIX #4: сообщаем UI что идёт перезапуск (wintun cleanup ≈ 30с).
	// Без этого /api/status возвращает running=false, warming=false — пользователь
	// не понимает что происходит. ClearRestarting вызывается через defer.
	h.server.SetRestarting(wintun.EstimateReadyAt())
	defer h.server.ClearRestarting()

	// BeforeRestart выполняет wintun cleanup (RecordStop + RemoveStaleTunAdapter + PollUntilFree).
	// Инъектируется через xray.Config.BeforeRestart из main.go —
	// api пакет больше не зависит от wintun напрямую.
	if h.xrayConfig.BeforeRestart != nil {
		if err := h.xrayConfig.BeforeRestart(h.server.lifecycleCtx, h.server.logger); err != nil {
			h.server.logger.Warn("BeforeRestart вернул ошибку: %v", err)
		}
	}

	// Применяем предварительно сгенерированный конфиг (или оставляем существующий).
	// Конфиг уже был проверен в handleApply ДО остановки sing-box — здесь просто
	// переименовываем .pending → рабочий файл. Это гарантирует атомарную замену.
	if tmpConfigPath != "" {
		if err := os.Rename(tmpConfigPath, h.xrayConfig.ConfigPath); err != nil {
			h.server.logger.Error("Не удалось применить конфиг: %v", err)
			_ = os.Remove(tmpConfigPath)
			setErr(err.Error())
			return
		}
		h.server.logger.Info("Конфиг sing-box обновлён")
	} else {
		h.server.logger.Warn("Конфиг не обновлялся — применяем с существующим")
	}

	// Запускаем sing-box.
	// BUG FIX: OnCrash в xrayCfg замыкается на старую переменную xrayManager из run().
	// После doApply xrayManager не обновляется — OnCrash вызывал бы Start() на старом
	// (уже остановленном) менеджере. Подменяем OnCrash: читаем актуальный менеджер
	// из h.server.config.XRayManager который всегда актуален.
	patchedCfg := h.xrayConfig
	srv := h.server
	patchedCfg.OnCrash = func(crashErr error, crashedManager xray.Manager) {
		srv.configMu.RLock()
		cur := srv.config.XRayManager
		srv.configMu.RUnlock()
		if cur != nil && h.xrayConfig.OnCrash != nil {
			h.xrayConfig.OnCrash(crashErr, crashedManager)
		}
	}
	newManager, err := xray.NewManager(patchedCfg, h.server.lifecycleCtx)
	if err != nil {
		h.server.logger.Error("Не удалось запустить sing-box: %v", err)
		setErr(err.Error())
		return
	}

	// BUG FIX #1: регистрируем менеджер ДО WaitForPort.
	// patchedCfg.OnCrash читает h.server.config.XRayManager — если sing-box упадёт
	// во время 15-секундного ожидания, OnCrash увидит nil и не запустит handleCrash.
	// Устанавливаем менеджер сразу после успешного Start() чтобы OnCrash работал с первой секунды.
	h.server.configMu.Lock()
	h.server.config.XRayManager = newManager
	h.server.configMu.Unlock()

	h.apply.mu.Lock()
	h.apply.lastPID = newManager.GetPID()
	h.apply.mu.Unlock()

	// Вместо фиксированного ожидания — опрашиваем Clash API каждые 200ms.
	// Экономит ~30 секунд на каждом перезапуске: sing-box обычно готов за 2–5с.
	if err := waitForSingBoxReady(h.server.lifecycleCtx, h.server.logger); err != nil {
		h.server.logger.Warn("Ожидание готовности sing-box: %v", err)
	}

	h.server.logger.Info("Sing-box перезапущен (PID: %d), правил: %d", newManager.GetPID(), len(snapshot.Rules))

	// B-11: логируем diff и обновляем lastApplied после успешного перезапуска
	h.mu.Lock()
	diff := computeRoutingDiff(h.lastApplied, snapshot)
	h.server.logger.Info("B-11: apply завершён: +%d, -%d, итого %d, reload_mode=restart",
		diff.RulesAdded, diff.RulesRemoved, diff.RulesTotal)
	h.lastApplied = snapshot
	h.routing = snapshot // App БАГ-7: синхронизируем h.routing с применённым конфигом
	h.mu.Unlock()
	h.apply.mu.Lock()
	h.apply.reloadMode = "restart" // B-11
	h.apply.mu.Unlock()

	// Всё прошло успешно — восстанавливаем прокси.
	restoreProxy = true
}

// handleApplyStatus GET /api/tun/apply/status — состояние последнего применения
func (h *TunHandlers) handleApplyStatus(w http.ResponseWriter, r *http.Request) {
	// BUG FIX: startedAt и estimatedDone читаются под мьютексом — они записываются
	// в handleApply под тем же мьютексом; чтение вне мьютекса было data race.
	h.apply.mu.Lock()
	running := h.apply.running
	lastErr := h.apply.lastErr
	validationError := h.apply.validationError
	lastPID := h.apply.lastPID
	startedAt := h.apply.startedAt
	estimatedDone := h.apply.estimatedDone
	reloadMode := h.apply.reloadMode // B-11
	h.apply.mu.Unlock()

	elapsedMs := int64(0)
	estimatedRemainMs := int64(0)
	if !startedAt.IsZero() {
		elapsedMs = time.Since(startedAt).Milliseconds()
	}
	if !estimatedDone.IsZero() && running {
		remaining := time.Until(estimatedDone).Milliseconds()
		if remaining < 0 {
			remaining = 0
		}
		estimatedRemainMs = remaining
	}
	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"running":             running,
		"last_err":            lastErr,
		"validation_error":    validationError,
		"last_pid":            lastPID,
		"elapsed_ms":          elapsedMs,
		"estimated_remain_ms": estimatedRemainMs,
		"estimated_total_ms":  5000,
		"reload_mode":         reloadMode, // B-11: "hotreload" | "restart" | ""
	})
}

// handleExport GET /api/tun/export — скачивает routing.json как файл
func (h *TunHandlers) handleExport(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	data, err := json.MarshalIndent(&config.RoutingConfig{
		DefaultAction: h.routing.DefaultAction,
		Rules:         h.routing.Rules,
	}, "", "  ")
	if err != nil {
		h.server.respondError(w, http.StatusInternalServerError, "marshal error")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="routing.json"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// handleImport POST /api/tun/import — загружает routing.json, валидирует и сохраняет
// Принимает multipart/form-data с полем "file" или application/json напрямую.
func (h *TunHandlers) handleImport(w http.ResponseWriter, r *http.Request) {
	var raw []byte

	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "multipart/form-data") {
		// Загрузка через <input type="file">
		if err := r.ParseMultipartForm(1 << 20); err != nil { // 1 MB max
			h.server.respondError(w, http.StatusBadRequest, "failed to parse form")
			return
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			h.server.respondError(w, http.StatusBadRequest, "field 'file' missing")
			return
		}
		defer f.Close()
		var err2 error
		raw, err2 = io.ReadAll(f)
		if err2 != nil {
			h.server.respondError(w, http.StatusBadRequest, "failed to read file")
			return
		}
	} else {
		// Прямая отправка JSON
		// BUG FIX #NEW-B: ограничиваем размер тела до 1MB чтобы предотвратить OOM.
		// Multipart-путь выше уже ограничен (ParseMultipartForm(1<<20)).
		// Без LimitReader злоумышленник из локальной сети может отправить гигантский
		// JSON и вызвать исчерпание памяти.
		var err error
		raw, err = io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MB max
		if err != nil {
			h.server.respondError(w, http.StatusBadRequest, "failed to read body")
			return
		}
	}

	var incoming config.RoutingConfig
	if err := json.Unmarshal(raw, &incoming); err != nil {
		h.server.respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	// Валидация и нормализация (аналогично handleBulkReplaceRules).
	// BUG FIX #3: без нормализации импортированные правила с URL-префиксами
	// (напр. "https://google.com" вместо "google.com") не матчились sing-box.
	if !isValidAction(incoming.DefaultAction) {
		h.server.respondError(w, http.StatusBadRequest, "invalid default_action")
		return
	}
	for i, rule := range incoming.Rules {
		val := config.NormalizeRuleValue(rule.Value)
		if val == "" {
			h.server.respondError(w, http.StatusBadRequest,
				fmt.Sprintf("rule[%d]: value is empty after normalization", i))
			return
		}
		if !isValidAction(rule.Action) {
			h.server.respondError(w, http.StatusBadRequest,
				fmt.Sprintf("rule[%d]: invalid action %q", i, rule.Action))
			return
		}
		ruleType := config.DetectRuleType(strings.ToLower(val))
		if ruleType != config.RuleTypeProcess {
			val = strings.ToLower(val)
		}
		incoming.Rules[i].Value = val
		incoming.Rules[i].Type = ruleType
	}

	// ВЫС-5: валидируем что импортированный конфиг генерирует корректный sing-box конфиг.
	// Проверяем ДО сохранения — не хотим затирать рабочий routing.json невалидным файлом.
	if h.xrayConfig.ExecutablePath != "" {
		tmpValidatePath := routingConfigPath + ".import_tmp"
		if genErr := config.GenerateSingBoxConfig(h.xrayConfig.SecretKeyPath, tmpValidatePath, &incoming); genErr == nil {
			if valErr := xray.ValidateSingBoxConfig(r.Context(), h.xrayConfig.ExecutablePath, tmpValidatePath); valErr != nil {
				_ = os.Remove(tmpValidatePath)
				h.server.respondError(w, http.StatusBadRequest, "импортированный конфиг невалиден: "+valErr.Error())
				return
			}
			_ = os.Remove(tmpValidatePath)
		}
		// Если GenerateSingBoxConfig вернула ошибку (нет secret.key и т.п.) — пропускаем валидацию
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if err := config.SaveRoutingConfig(routingConfigPath, &incoming); err != nil {
		h.server.respondError(w, http.StatusInternalServerError, "failed to save: "+err.Error())
		return
	}
	h.routing = &incoming

	h.server.respondJSON(w, http.StatusOK, MessageResponse{
		Success: true,
		Message: fmt.Sprintf("imported %d rules", len(incoming.Rules)),
	})
}
