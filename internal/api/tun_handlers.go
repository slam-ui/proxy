package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"proxyclient/internal/config"
	"proxyclient/internal/engine"
	"proxyclient/internal/logger"
	"proxyclient/internal/proxy"
	"proxyclient/internal/wintun"
	"proxyclient/internal/xray"

	"github.com/gorilla/mux"
)

var routingConfigPath = config.DataDir + "/routing.json"
var errSaveRoutingConfig = errors.New("не удалось сохранить правила")

// clashAPIBaseURL — базовый URL Clash API. Переменная (не константа) чтобы
// тесты могли подменить его на адрес локального mock-сервера.
var clashAPIBaseURL = "http://" + config.ClashAPIAddr

// tryHotReload перезагружает конфиг sing-box через Clash API без остановки процесса.
// Возвращает nil при успехе, ошибку если API недоступен → вызывающий делает полный перезапуск.
func tryHotReload(configPath string) error {
	absPath, err := filepath.Abs(configPath)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{"path": absPath, "force": true})
	req, err := http.NewRequest(http.MethodPut,
		clashAPIBaseURL+"/configs", bytes.NewReader(body))
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

// copyConfigFile копирует файл побайтово — fallback когда os.Rename не работает
// (разные тома, файл заблокирован антивирусом).
func copyConfigFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open src: %w", err)
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("open dst: %w", err)
	}
	defer func() {
		_ = out.Close()
	}()

	if _, err := io.Copy(out, in); err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("copy: %w", err)
	}
	return out.Sync()
}

// WaitForSingBoxReady опрашивает Clash API каждые 200ms пока sing-box не ответит.
// Экспортирована для использования в app.go (startBackground) — единая точка проверки
// готовности sing-box вместо разрозненных WaitForPort с разными таймаутами.
func WaitForSingBoxReady(ctx context.Context, log logger.Logger) error {
	return waitForSingBoxReady(ctx, log)
}

// waitForSingBoxReady — внутренняя реализация.
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
			resp, err := client.Get(clashAPIBaseURL + "/")
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
	pendingApply    bool // FIX: правила изменились пока apply выполнялся — нужен повторный apply
	pendingFull     bool // отложенный apply должен быть полным restart (смена сервера)
	pendingWithFile bool // отложенный apply должен применить уже записанный config
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

func cloneRoutingConfig(src *config.RoutingConfig) *config.RoutingConfig {
	if src == nil {
		return config.DefaultRoutingConfig()
	}
	dst := &config.RoutingConfig{
		DefaultAction: src.DefaultAction,
		BypassEnabled: src.BypassEnabled,
	}
	if src.Rules != nil {
		dst.Rules = append([]config.RoutingRule(nil), src.Rules...)
	}
	if src.DNS != nil {
		dns := *src.DNS
		dst.DNS = &dns
	}
	return dst
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

	// FIX 21: используем составной ключ {Value, Action, Type} вместо только Value.
	// Без Type/Action изменение action существующего правила не обнаруживалось как diff.
	type ruleKey struct {
		Value  string
		Action config.RuleAction
		Type   config.RuleType
	}
	oldSet := make(map[ruleKey]struct{}, len(old.Rules))
	for _, r := range old.Rules {
		oldSet[ruleKey{r.Value, r.Action, r.Type}] = struct{}{}
	}
	newSet := make(map[ruleKey]struct{}, len(newCfg.Rules))
	for _, r := range newCfg.Rules {
		newSet[ruleKey{r.Value, r.Action, r.Type}] = struct{}{}
	}

	added := 0
	for k := range newSet {
		if _, ok := oldSet[k]; !ok {
			added++
		}
	}
	removed := 0
	for k := range oldSet {
		if _, ok := newSet[k]; !ok {
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
	// newManagerFn — фабрика xray.Manager. nil → xray.NewManager (продакшн).
	// Тесты подменяют это поле чтобы запускать mock вместо реального sing-box.
	newManagerFn func(cfg xray.Config, ctx context.Context) (xray.Manager, error)
}

// SetupTunRoutes регистрирует маршруты
func (s *Server) SetupTunRoutes(xrayCfg xray.Config) *TunHandlers {
	routing, err := config.LoadRoutingConfig(routingConfigPath)
	if err != nil {
		s.logger.Warn("Не удалось загрузить routing config: %v", err)
		routing = config.DefaultRoutingConfig()
	}
	smartSortRoutingRules(routing.Rules)

	h := &TunHandlers{
		server:       s,
		xrayConfig:   xrayCfg,
		proxyManager: s.config.ProxyManager,
		routing:      routing,
		// FIX: инициализируем lastApplied текущим состоянием чтобы первый diff
		// показывал реальные изменения, а не "nil→process rules" → не форсировал рестарт
		// при каждом TriggerApply пока sing-box уже запущен с правильным конфигом.
		lastApplied: cloneRoutingConfig(routing),
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

	// Сохраняем ссылку чтобы handleConnect мог вызвать TriggerApply при смене сервера.
	s.tunHandlers = h

	return h
}

func (h *TunHandlers) markPendingApplyLocked(forceRestart, withCurrentConfig bool) {
	h.apply.pendingApply = true
	h.apply.pendingFull = h.apply.pendingFull || forceRestart
	if forceRestart {
		h.apply.pendingWithFile = false
	} else {
		h.apply.pendingWithFile = h.apply.pendingWithFile || withCurrentConfig
	}
	h.apply.lastErr = ""
}

func (h *TunHandlers) logQueuedApply(forceRestart, withCurrentConfig bool, source string) {
	if source == "" {
		source = "apply"
	}
	mode := "обычный apply"
	if forceRestart {
		mode = "полный перезапуск"
	} else if withCurrentConfig {
		mode = "применение текущего конфига"
	}
	h.server.logger.Info("%s: %s поставлен в очередь", source, mode)
}

func (h *TunHandlers) queueApply(forceRestart, withCurrentConfig bool, source string) {
	h.apply.mu.Lock()
	h.markPendingApplyLocked(forceRestart, withCurrentConfig)
	h.apply.mu.Unlock()
	h.logQueuedApply(forceRestart, withCurrentConfig, source)
}

func (h *TunHandlers) drainQueuedApply() {
	h.apply.mu.Lock()
	pending := h.apply.pendingApply
	running := h.apply.running
	forceRestart := h.apply.pendingFull
	withCurrentConfig := h.apply.pendingWithFile && !forceRestart
	h.apply.mu.Unlock()

	if !pending || running || h.server.IsRestarting() || h.server.IsWarming() {
		return
	}

	var err error
	switch {
	case forceRestart:
		err = h.TriggerApplyFull()
	case withCurrentConfig:
		err = h.TriggerApplyWithConfig()
	default:
		err = h.TriggerApply()
	}
	if err != nil {
		h.server.logger.Warn("drainQueuedApply: не удалось запустить отложенное применение: %v", err)
	}
}

// TriggerApply запускает перегенерацию конфига и полный перезапуск sing-box без HTTP-контекста.
// Hot-reload намеренно не используется: изменения правил, DNS, geosite и серверов должны
// проходить один и тот же полный restart-путь.
func (h *TunHandlers) TriggerApply() error {
	return h.TriggerApplyFull()
}

// TriggerApplyWithConfig запускает перезапуск sing-box с уже готовым конфигом на диске.
// В отличие от TriggerApply, НЕ перегенерирует конфиг — использует тот что уже лежит
// по configPath. Предназначен для applyTURNMode: конфиг уже записан с TURN override,
// перегенерация через GenerateSingBoxConfig уничтожила бы его (bug: TURN не работал).
func (h *TunHandlers) TriggerApplyWithConfig() error {
	// FIX 13: не запускаем apply пока идёт TUN crash-recovery — аналогично TriggerApply.
	if h.server.IsRestarting() {
		h.queueApply(false, true, "TriggerApplyWithConfig")
		return nil
	}
	if h.server.IsWarming() {
		h.queueApply(false, true, "TriggerApplyWithConfig")
		return nil
	}
	h.apply.mu.Lock()
	if h.apply.running {
		// FIX: аналогично TriggerApply — ставим pendingApply вместо ошибки.
		h.markPendingApplyLocked(false, true)
		h.apply.mu.Unlock()
		h.logQueuedApply(false, true, "TriggerApplyWithConfig")
		return nil
	}
	h.apply.running = true
	h.apply.pendingApply = false
	h.apply.pendingFull = false
	h.apply.pendingWithFile = false
	h.apply.lastErr = ""
	h.apply.reloadMode = ""
	h.apply.startedAt = time.Now()
	h.apply.estimatedDone = time.Now().Add(5 * time.Second)
	h.apply.mu.Unlock()

	h.mu.RLock()
	snapshot := cloneRoutingConfig(h.routing)
	h.mu.RUnlock()

	// Используем существующий конфиг (уже записан вызывающей стороной с TURN override).
	// tmpConfigPath="" означает для doApply: не переименовывать, применять текущий config.
	go h.doApply(snapshot, "", true)
	return nil
}

// RulesResponse ответ GET /api/tun/rules
type RulesResponse struct {
	DefaultAction config.RuleAction    `json:"default_action"`
	Rules         []config.RoutingRule `json:"rules"`
	BypassEnabled bool                 `json:"bypass_enabled,omitempty"`
	DNS           *config.DNSConfig    `json:"dns,omitempty"` // FIX 26: возвращаем DNS вместе с правилами
}

// handleListRules GET /api/tun/rules
func (h *TunHandlers) handleListRules(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	resp := RulesResponse{
		DefaultAction: h.routing.DefaultAction,
		Rules:         h.routing.Rules,
		BypassEnabled: h.routing.BypassEnabled,
		DNS:           h.routing.DNS, // FIX 26
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

	for _, rule := range h.routing.Rules {
		// FIX 25: для process-правил сравниваем без учёта регистра (Windows нечувствителен к регистру).
		// Без этого "telegram.exe" и "Telegram.exe" добавлялись как два разных правила.
		dup := false
		if ruleType == config.RuleTypeProcess {
			dup = strings.EqualFold(rule.Value, val)
		} else {
			dup = rule.Value == val
		}
		if dup {
			h.mu.Unlock()
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
	smartSortRoutingRules(h.routing.Rules)
	// FIX Bug7: снимаем копию для сохранения и освобождаем мьютекс ДО I/O.
	// Ранее h.mu удерживался на время SaveRoutingConfig (WriteAtomic с повторами до 1с)
	// → все читатели (handleListRules, TriggerApply) блокировались на это время.
	routingCopy := cloneRoutingConfig(h.routing)
	h.mu.Unlock()

	if err := config.SaveRoutingConfig(routingConfigPath, routingCopy); err != nil {
		// Откат по точному значению: список мог быть пересортирован smartSortRoutingRules.
		h.mu.Lock()
		for i, rule := range h.routing.Rules {
			if rule == newRule {
				h.routing.Rules = append(h.routing.Rules[:i], h.routing.Rules[i+1:]...)
				break
			}
		}
		h.mu.Unlock()
		h.server.logger.Error("Не удалось сохранить routing config: %v", err)
		h.server.respondError(w, http.StatusInternalServerError, "не удалось сохранить правило")
		return
	}

	// Применяем новый конфиг — перегенерируем sing-box config и перезапускаем.
	// Без этого правило сохраняется в файл, но sing-box продолжает работать со старым конфигом.
	// ВАЖНО: вызывается ПОСЛЕ освобождения h.mu, иначе TriggerApply → h.mu.RLock() = дедлок.
	// BUG-6 FIX: добавляем apply_error в ответ — клиент знает что apply не запустился.
	applyErr := ""
	if err := h.TriggerApply(); err != nil {
		h.server.logger.Warn("handleAddRule: TriggerApply: %v", err)
		applyErr = err.Error()
	}
	h.server.respondJSON(w, http.StatusCreated, map[string]interface{}{
		"message":     "правило добавлено",
		"rule":        newRule,
		"apply_error": applyErr,
	})
}

// handleBulkReplaceRules тело PUT /api/tun/rules
type BulkReplaceRequest struct {
	DefaultAction config.RuleAction    `json:"default_action"`
	Rules         []config.RoutingRule `json:"rules"`
	BypassEnabled *bool                `json:"bypass_enabled,omitempty"`
	DNS           *config.DNSConfig    `json:"dns,omitempty"` // FIX 26: принимаем DNS вместе с правилами
}

func normalizeRoutingRules(rules []config.RoutingRule) error {
	// Валидируем и нормализуем каждое правило.
	// BUG FIX: handleAddRule вызывает NormalizeRuleValue + DetectRuleType,
	// bulk replace не вызывал — правила сохранялись с URL-префиксами и
	// неверными типами, sing-box их не матчил.
	// BUG FIX #NEW-1: добавлено приведение к lowercase для non-process правил —
	// аналогично handleAddRule и handleImport. Без этого drag-and-drop реорганизация
	// правил через PUT /api/tun/rules сохраняла uppercase домены которые sing-box
	// не матчил (domain matching регистрозависимый в sing-box).
	for i, rule := range rules {
		val := config.NormalizeRuleValue(rule.Value)
		if val == "" {
			return fmt.Errorf("правило #%d: пустое значение после нормализации", i+1)
		}
		if !isValidAction(rule.Action) {
			return fmt.Errorf("правило #%d: неверный action (proxy|direct|block)", i+1)
		}
		ruleType := config.DetectRuleType(strings.ToLower(val))
		// FIX: process rules сохраняем в оригинальном регистре (Windows process_name чувствителен).
		// Домены и IP приводим к lowercase.
		if ruleType != config.RuleTypeProcess {
			val = strings.ToLower(val)
		}
		rules[i].Value = val
		rules[i].Type = ruleType
	}
	smartSortRoutingRules(rules)
	return nil
}

func smartSortRoutingRules(rules []config.RoutingRule) {
	actionRank := func(a config.RuleAction) int {
		switch a {
		case config.ActionBlock:
			return 0
		case config.ActionDirect:
			return 1
		case config.ActionProxy:
			return 2
		default:
			return 3
		}
	}
	typeRank := func(t config.RuleType) int {
		switch t {
		case config.RuleTypeIP:
			return 0
		case config.RuleTypeDomain:
			return 1
		case config.RuleTypeProcess:
			return 2
		case config.RuleTypeGeosite:
			return 3
		default:
			return 4
		}
	}
	sort.SliceStable(rules, func(i, j int) bool {
		ai, aj := actionRank(rules[i].Action), actionRank(rules[j].Action)
		if ai != aj {
			return ai < aj
		}
		ti, tj := typeRank(rules[i].Type), typeRank(rules[j].Type)
		if ti != tj {
			return ti < tj
		}
		return strings.ToLower(rules[i].Value) < strings.ToLower(rules[j].Value)
	})
}

func (h *TunHandlers) replaceRoutingAndApply(incoming config.RoutingConfig) (int, string, error) {
	if incoming.DefaultAction == "" {
		incoming.DefaultAction = config.ActionProxy
	}
	if !isValidAction(incoming.DefaultAction) {
		return 0, "", fmt.Errorf("default_action: proxy | direct | block")
	}
	if err := normalizeRoutingRules(incoming.Rules); err != nil {
		return 0, "", err
	}

	h.mu.Lock()
	oldRouting := cloneRoutingConfig(h.routing)

	h.routing.DefaultAction = incoming.DefaultAction
	h.routing.Rules = incoming.Rules
	h.routing.BypassEnabled = incoming.BypassEnabled
	h.routing.DNS = incoming.DNS

	// FIX Bug7: освобождаем мьютекс до I/O.
	routingCopy := cloneRoutingConfig(h.routing)
	h.mu.Unlock()

	if err := config.SaveRoutingConfig(routingConfigPath, routingCopy); err != nil {
		h.mu.Lock()
		h.routing = oldRouting
		h.mu.Unlock()
		return 0, "", fmt.Errorf("%w: %v", errSaveRoutingConfig, err)
	}

	// ВАЖНО: вызывается ПОСЛЕ освобождения h.mu, иначе TriggerApply → h.mu.RLock() = дедлок.
	applyErr := ""
	if err := h.TriggerApply(); err != nil {
		applyErr = err.Error()
	}
	return len(incoming.Rules), applyErr, nil
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

	// Валидируем default_action (пустая строка → оставляем текущий)
	if req.DefaultAction != "" && !isValidAction(req.DefaultAction) {
		h.server.respondError(w, http.StatusBadRequest, "default_action: proxy | direct | block")
		return
	}

	h.mu.RLock()
	incoming := config.RoutingConfig{
		DefaultAction: h.routing.DefaultAction,
		Rules:         req.Rules,
		BypassEnabled: h.routing.BypassEnabled,
		DNS:           h.routing.DNS,
	}
	h.mu.RUnlock()
	if req.DefaultAction != "" {
		incoming.DefaultAction = req.DefaultAction
	}
	if req.BypassEnabled != nil {
		incoming.BypassEnabled = *req.BypassEnabled
	}
	if req.DNS != nil {
		incoming.DNS = req.DNS
	}

	count, applyErr, err := h.replaceRoutingAndApply(incoming)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errSaveRoutingConfig) {
			status = http.StatusInternalServerError
			h.server.logger.Error("Bulk replace: не удалось сохранить routing config: %v", err)
		}
		h.server.respondError(w, status, err.Error())
		return
	}
	if applyErr != "" {
		h.server.logger.Warn("handleBulkReplaceRules: TriggerApply: %v", applyErr)
	}
	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"message":     "правила обновлены",
		"count":       count,
		"apply_error": applyErr,
	})
}

// handleDeleteRule DELETE /api/tun/rules/{value}
func (h *TunHandlers) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	value := strings.ToLower(vars["value"])

	h.mu.Lock()

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
		h.mu.Unlock()
		h.server.respondError(w, http.StatusNotFound, "правило не найдено")
		return
	}

	oldRules := h.routing.Rules // сохраняем для отката
	h.routing.Rules = newRules
	// FIX Bug7: освобождаем мьютекс до I/O.
	routingCopy := cloneRoutingConfig(h.routing)
	h.mu.Unlock()

	if err := config.SaveRoutingConfig(routingConfigPath, routingCopy); err != nil {
		h.mu.Lock()
		h.routing.Rules = oldRules // откат
		h.mu.Unlock()
		h.server.logger.Error("Не удалось сохранить routing config: %v", err)
		h.server.respondError(w, http.StatusInternalServerError, "не удалось сохранить изменения")
		return
	}

	// BUG-1+6 FIX: TriggerApply ДО respondJSON.
	// ВАЖНО: вызывается ПОСЛЕ освобождения h.mu, иначе TriggerApply → h.mu.RLock() = дедлок.
	applyErr := ""
	if err := h.TriggerApply(); err != nil {
		h.server.logger.Warn("handleDeleteRule: TriggerApply: %v", err)
		applyErr = err.Error()
	}
	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"message":     "правило удалено",
		"success":     true,
		"apply_error": applyErr,
	})
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
	// FIX Bug7: освобождаем мьютекс до I/O.
	routingCopy := cloneRoutingConfig(h.routing)
	h.mu.Unlock()

	if err := config.SaveRoutingConfig(routingConfigPath, routingCopy); err != nil {
		h.mu.Lock()
		h.routing.DefaultAction = oldAction // откат
		h.mu.Unlock()
		h.server.logger.Error("Не удалось сохранить routing config: %v", err)
		h.server.respondError(w, http.StatusInternalServerError, "не удалось сохранить изменения")
		return
	}

	// BUG-1+6 FIX: TriggerApply ДО respondJSON.
	// FIX 24: без TriggerApply изменение default_action сохраняется в файл
	// но sing-box продолжает работать со старым конфигом.
	applyErr := ""
	if err := h.TriggerApply(); err != nil {
		h.server.logger.Warn("handleSetDefault: TriggerApply: %v", err)
		applyErr = err.Error()
	}
	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"message":     "дефолтное действие обновлено",
		"success":     true,
		"apply_error": applyErr,
	})
}

// handleApply POST /api/tun/apply — запускает перезапуск асинхронно и сразу возвращает ответ
func (h *TunHandlers) handleApply(w http.ResponseWriter, r *http.Request) {
	// Блокируем apply пока handleCrash выполняет TUN recovery:
	// оба пути вызывают wintun.RemoveStaleTunAdapter + PollUntilFree + запуск sing-box,
	// параллельный запуск даёт двойной sing-box → повторный TUN conflict.
	if h.server.IsRestarting() {
		h.queueApply(true, false, "handleApply")
		h.server.respondJSON(w, http.StatusAccepted, map[string]interface{}{
			"message": "применение поставлено в очередь до завершения восстановления TUN",
			"queued":  true,
		})
		return
	}
	if h.server.IsWarming() {
		h.queueApply(true, false, "handleApply")
		h.server.respondJSON(w, http.StatusAccepted, map[string]interface{}{
			"message": "применение поставлено в очередь до запуска sing-box",
			"queued":  true,
		})
		return
	}

	h.apply.mu.Lock()
	if h.apply.running {
		// FIX: вместо ошибки ставим pendingApply — apply запустится автоматически
		// после завершения текущего. Раньше пользователь получал ошибку и правила
		// не применялись до ручного перезапуска.
		h.markPendingApplyLocked(true, false)
		h.apply.mu.Unlock()
		h.logQueuedApply(true, false, "handleApply")
		h.server.respondJSON(w, http.StatusAccepted, map[string]interface{}{
			"message": "применение поставлено в очередь",
			"queued":  true,
		})
		return
	}
	h.apply.running = true
	h.apply.pendingApply = false
	h.apply.pendingFull = false
	h.apply.pendingWithFile = false
	h.apply.lastErr = ""
	h.apply.validationError = "" // БАГ 13: сбрасываем ошибку валидации при каждом новом apply
	h.apply.reloadMode = ""
	h.apply.startedAt = time.Now()
	h.apply.estimatedDone = time.Now().Add(5 * time.Second) // минимальный буфер; готовность через Clash API probe
	h.apply.mu.Unlock()

	h.mu.RLock()
	snapshot := cloneRoutingConfig(h.routing)
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

	go h.doApply(snapshot, tmpConfigPath, true)
}

// TriggerApplyFull запускает ПОЛНЫЙ перезапуск sing-box, минуя hot-reload.
// Используется при смене VLESS-сервера: hot-reload через Clash API НЕ переинициализирует
// outbound соединения → sing-box продолжает туннелировать через старый сервер.
// В остальном идентичен TriggerApply.
func (h *TunHandlers) TriggerApplyFull() error {
	if h.server.IsRestarting() {
		h.queueApply(true, false, "TriggerApplyFull")
		return nil
	}
	if h.server.IsWarming() {
		h.queueApply(true, false, "TriggerApplyFull")
		return nil
	}
	h.apply.mu.Lock()
	if h.apply.running {
		// FIX: аналогично TriggerApply — ставим pendingApply вместо ошибки.
		h.markPendingApplyLocked(true, false)
		h.apply.mu.Unlock()
		h.logQueuedApply(true, false, "TriggerApplyFull")
		return nil
	}
	h.apply.running = true
	h.apply.pendingApply = false
	h.apply.pendingFull = false
	h.apply.pendingWithFile = false
	h.apply.lastErr = ""
	h.apply.reloadMode = ""
	h.apply.startedAt = time.Now()
	h.apply.estimatedDone = time.Now().Add(5 * time.Second)
	h.apply.mu.Unlock()

	h.mu.RLock()
	snapshot := cloneRoutingConfig(h.routing)
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

	go h.doApply(snapshot, tmpConfigPath, true /* forceRestart */)
	return nil
}

// doApply выполняет перезапуск sing-box в фоновой горутине.
// tmpConfigPath — путь к предварительно сгенерированному конфигу (или "" если использовать существующий).
// forceRestart=true пропускает попытку hot-reload и всегда выполняет полный перезапуск.
func (h *TunHandlers) doApply(snapshot *config.RoutingConfig, tmpConfigPath string, forceRestart bool) {
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
		pending := h.apply.pendingApply
		pendingFull := h.apply.pendingFull
		pendingWithFile := h.apply.pendingWithFile && !pendingFull
		h.apply.pendingApply = false
		h.apply.pendingFull = false
		h.apply.pendingWithFile = false
		h.apply.running = false
		h.apply.mu.Unlock()

		// FIX: если за время выполнения apply были изменения правил (pendingApply=true),
		// запускаем повторный apply с актуальным состоянием. Это решает проблему когда
		// пользователь быстро добавляет/удаляет правила — раньше промежуточные изменения
		// сохранялись на диск но не применялись к sing-box до перезапуска приложения.
		if pending {
			mode := "актуальными правилами"
			var err error
			switch {
			case pendingFull:
				mode = "полным перезапуском"
				err = h.TriggerApplyFull()
			case pendingWithFile:
				mode = "текущим конфигом"
				err = h.TriggerApplyWithConfig()
			default:
				err = h.TriggerApply()
			}
			h.server.logger.Info("doApply: обнаружен pendingApply — запускаем повторный apply с %s", mode)
			if err != nil {
				h.server.logger.Warn("doApply: повторный TriggerApply не удался: %v", err)
			}
		}
	}()

	if engine.EnsureInProgress() {
		h.server.logger.Info("apply ожидает завершения проверки sing-box.exe...")
		if err := engine.WaitForEnsure(h.server.lifecycleCtx); err != nil {
			setErr("ожидание готовности sing-box.exe: " + err.Error())
			return
		}
	}

	// FIX 30: вычисляем diff один раз на уровне функции — используется и в restart пути.
	diff := computeRoutingDiff(h.lastApplied, snapshot)

	// Hot reload отключён: при изменении правил, DNS, geosite или сервера нужен полный
	// перезапуск sing-box, чтобы не оставались старые outbound/TUN/process состояния.
	{
		h.server.configMu.RLock()
		hotMgr := h.server.config.XRayManager
		h.server.configMu.RUnlock()

		skipHotReload := diff.ProcessRulesChanged || forceRestart
		if forceRestart {
			h.server.logger.Info("Apply: полный перезапуск sing-box (hot-reload отключён)")
		} else if diff.ProcessRulesChanged {
			h.server.logger.Info("Process-правила изменились (старые: %v, новые: %v) — пропускаем hot-reload",
				hasProcessRules(h.lastApplied), hasProcessRules(snapshot))
		}

		if tmpConfigPath != "" && hotMgr != nil && hotMgr.IsRunning() && !skipHotReload {
			if err := tryHotReload(tmpConfigPath); err == nil {
				h.server.logger.Info("Hot reload конфига успешен, перезапуск не нужен")
				finalPath := h.xrayConfig.ConfigPath

				// BUG FIX #3: при ошибке Rename — пробуем copyConfigFile как fallback.
				// Rename может падать если src и dst на разных томах или файл заблокирован.
				// Если и копия провалилась — не обновляем h.lastApplied: следующий apply
				// увидит расхождение и выполнит полный перезапуск.
				renamed := true
				if renameErr := os.Rename(tmpConfigPath, finalPath); renameErr != nil {
					h.server.logger.Error("Hot reload: Rename провалился: %v — пробуем copy fallback", renameErr)
					if copyErr := copyConfigFile(tmpConfigPath, finalPath); copyErr != nil {
						h.server.logger.Error("Hot reload: copy fallback тоже провалился: %v — конфиг на диске не обновлён", copyErr)
						_ = os.Remove(tmpConfigPath)
						setErr("hot reload применён в память, но конфиг на диске не обновлён")
						renamed = false
					} else {
						_ = os.Remove(tmpConfigPath)
						h.server.logger.Info("Hot reload: конфиг сохранён через copy fallback")
					}
				}

				if !renamed {
					// Конфиг на диске старый — не помечаем apply успешным.
					return
				}

				h.mu.Lock()
				// FIX 30: используем diff уже вычисленный выше, не пересчитываем.
				// FIX 50: h.routing = snapshot убрано — h.routing должен оставаться
				// актуальным in-memory состоянием, не заменяться замороженным снапшотом.
				h.server.logger.Info("B-11: apply завершён: +%d, -%d, итого %d, reload_mode=hotreload",
					diff.RulesAdded, diff.RulesRemoved, diff.RulesTotal)
				h.lastApplied = snapshot
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

	// БАГ 14: приостанавливаем Proxy Guard на 90 секунд — за это время sing-box перезапустится.
	// Без паузы Guard видит что прокси выключен и немедленно восстанавливает его → двойная
	// запись в реестр Windows и шумные WARN-логи "обнаружено отключение прокси".
	h.proxyManager.PauseGuard(90 * time.Second)
	defer h.proxyManager.ResumeGuard()

	// Запоминаем состояние прокси и отключаем его на время рестарта,
	// чтобы трафик не уходил в недоступный sing-box.
	proxyWasEnabled := h.proxyManager.IsEnabled()
	proxyConfig := h.proxyManager.GetConfig()
	if proxyWasEnabled {
		if err := h.proxyManager.Disable(); err != nil {
			h.server.logger.Warn("Не удалось отключить прокси перед рестартом: %v", err)
		}
	}

	// Восстанавливаем прокси при выходе из функции во всех случаях кроме успеха.
	// FIX Bug3: ранее restoreProxy=false по умолчанию: при любой ошибке (BeforeRestart,
	// os.Rename, NewManager) прокси оставался выключен и пользователь терял интернет
	// до следующего ручного apply.
	// Теперь: skipProxyRestore=false → defer восстанавливает прокси при любом return.
	// При успешном запуске sing-box: skipProxyRestore=true → прокси восстановит handleCrash
	// или он уже будет восстановлен через sing-box.
	skipProxyRestore := false
	defer func() {
		if !skipProxyRestore && proxyWasEnabled {
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
	readyAt := wintun.EstimateReadyAt()
	h.server.SetRestarting(readyAt)
	defer h.server.ClearRestarting()

	// BUG FIX: обновляем estimatedDone реальным ETA от wintun.
	// Ранее estimatedDone = Now()+5с (установлено в handleApply), но wintun cleanup
	// занимает 30-120с. /api/tun/apply/status возвращал estimated_remain_ms=0 через 5с,
	// фронтенд показывал "0с" весь оставшийся период — пользователь думал что зависло.
	h.apply.mu.Lock()
	h.apply.estimatedDone = readyAt
	h.apply.mu.Unlock()

	// BeforeRestart выполняет wintun cleanup (RecordStop + RemoveStaleTunAdapter + PollUntilFree).
	// Инъектируется через xray.Config.BeforeRestart из main.go —
	// api пакет больше не зависит от wintun напрямую.
	if h.xrayConfig.BeforeRestart != nil {
		if err := h.xrayConfig.BeforeRestart(h.server.lifecycleCtx, h.server.logger); err != nil {
			// FIX 23: прерываем apply если ошибка не связана с отменой контекста.
			// Context cancellation (app shutdown) — штатная ситуация, логируем и выходим.
			// Реальные ошибки (wintun timeout, OS error) — sing-box запускать нельзя.
			if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				h.server.logger.Error("BeforeRestart вернул ошибку, отменяем apply: %v", err)
				setErr("BeforeRestart: " + err.Error())
				return
			}
			h.server.logger.Warn("BeforeRestart прерван контекстом: %v", err)
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
	startManager := h.newManagerFn
	if startManager == nil {
		startManager = xray.NewManager
	}

	// Превентивное удаление dns_cache.db перед каждым стартом sing-box через doApply.
	// Предотвращает FATAL "initialize cache-file: timeout" если файл остался заблокированным
	// от предыдущего экземпляра (BeforeRestart делает taskkill без graceful stop).
	if removeErr := os.Remove(config.DNSCacheFile); removeErr == nil {
		h.server.logger.Info("dns_cache.db удалён (превентивная очистка перед apply)")
	}

	newManager, err := startManager(patchedCfg, h.server.lifecycleCtx)
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
	// BUG FIX: добавляем быстрый выход если sing-box умер во время ожидания.
	// Раньше: waitForSingBoxReady ждал 60с даже если sing-box упал через 1с.
	// Новый подход: создаём контекст с отменой при смерти процесса — выходим сразу.
	// Это также устраняет race condition с startBackground.WaitForSingBoxReady:
	// оба 60s таймера больше не срабатывают одновременно когда sing-box упал быстро.
	waitStart := time.Now()
	waitCtx, waitCancel := context.WithCancel(h.server.lifecycleCtx)
	go func() {
		// Ждём завершения sing-box — отменяем waitCtx как только процесс умер.
		// newManager.Wait() блокируется до завершения процесса (внутри select на done chan).
		_ = newManager.Wait()
		waitCancel()
	}()
	waitErr := waitForSingBoxReady(waitCtx, h.server.logger)
	waitCancel() // освобождаем горутину если waitForSingBoxReady вернулся раньше
	if waitErr != nil {
		elapsed := time.Since(waitStart)
		// BUG FIX 3: проверяем IsRunning() — если sing-box упал во время ожидания,
		// 60s таймер срабатывает для уже мёртвого процесса (лог "не ответил за 60с" через 800мс).
		// Отличаем краш от реального тайм-аута: при краше прекращаем apply с ошибкой.
		if !newManager.IsRunning() {
			if errors.Is(waitErr, context.Canceled) || h.server.IsRestarting() {
				h.server.logger.Info("sing-box завершился во время ожидания готовности (прошло %v) — crash-recovery продолжит запуск",
					elapsed.Round(time.Second))
				setErr("")
			} else {
				h.server.logger.Error("sing-box упал во время ожидания готовности (прошло %v): %v",
					elapsed.Round(time.Second), waitErr)
				setErr("sing-box упал сразу после запуска")
			}
			return
		}
		if !errors.Is(waitErr, context.Canceled) {
			h.server.logger.Warn("Ожидание готовности sing-box: %v (прошло %v с момента запуска)",
				waitErr, elapsed.Round(time.Second))
		}
	}

	h.server.logger.Info("Sing-box перезапущен (PID: %d), правил: %d", newManager.GetPID(), len(snapshot.Rules))

	// B-11: логируем diff и обновляем lastApplied после успешного перезапуска.
	// FIX 50: h.routing = snapshot убрано — h.routing остаётся актуальным состоянием.
	// FIX 30: diff уже вычислен в hotreload-блоке выше, переиспользуем.
	h.mu.Lock()
	h.server.logger.Info("B-11: apply завершён: +%d, -%d, итого %d, reload_mode=restart",
		diff.RulesAdded, diff.RulesRemoved, diff.RulesTotal)
	h.lastApplied = snapshot
	h.mu.Unlock()
	h.apply.mu.Lock()
	h.apply.reloadMode = "restart" // B-11
	h.apply.mu.Unlock()

	// Всё прошло успешно — восстанавливаем системный прокси Windows.
	// BUG FIX #1: ранее skipProxyRestore=true выставлялось без Enable() —
	// системный прокси оставался выключен навсегда. sing-box НЕ управляет
	// Windows Registry proxy, поэтому нужно явное Enable() здесь.
	// Подавляем defer чтобы не вызвать Enable() дважды.
	if proxyWasEnabled {
		if err := h.proxyManager.Enable(proxyConfig); err != nil {
			h.server.logger.Error("doApply: не удалось восстановить прокси после перезапуска: %v", err)
		}
	}
	skipProxyRestore = true
}

// handleApplyStatus GET /api/tun/apply/status — состояние последнего применения
func (h *TunHandlers) handleApplyStatus(w http.ResponseWriter, r *http.Request) {
	// BUG FIX: startedAt и estimatedDone читаются под мьютексом — они записываются
	// в handleApply под тем же мьютексом; чтение вне мьютекса было data race.
	h.apply.mu.Lock()
	running := h.apply.running
	pendingApply := h.apply.pendingApply
	pendingFull := h.apply.pendingFull
	pendingWithFile := h.apply.pendingWithFile
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
		"pending_apply":       pendingApply, // FIX: клиент видит что есть отложенный apply
		"pending_full":        pendingFull,
		"pending_with_file":   pendingWithFile,
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
		BypassEnabled: h.routing.BypassEnabled,
		DNS:           h.routing.DNS, // FIX 14: экспортируем DNS настройки вместе с правилами
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
	if err := normalizeRoutingRules(incoming.Rules); err != nil {
		h.server.respondError(w, http.StatusBadRequest, err.Error())
		return
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

	if err := config.SaveRoutingConfig(routingConfigPath, &incoming); err != nil {
		h.mu.Unlock()
		h.server.respondError(w, http.StatusInternalServerError, "failed to save: "+err.Error())
		return
	}
	h.routing = cloneRoutingConfig(&incoming)
	h.mu.Unlock()

	h.server.respondJSON(w, http.StatusOK, MessageResponse{
		Success: true,
		Message: fmt.Sprintf("imported %d rules", len(incoming.Rules)),
	})

	// FIX 22: применяем импортированные правила — без TriggerApply sing-box работает
	// со старым конфигом до ручного перезапуска. Вызывается ПОСЛЕ Unlock.
	if err := h.TriggerApply(); err != nil {
		h.server.logger.Warn("handleImport: TriggerApply: %v", err)
	}
}
