package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"proxyclient/internal/config"
	"proxyclient/internal/netutil"
	"proxyclient/internal/proxy"
	"proxyclient/internal/xray"

	"github.com/gorilla/mux"
)

const routingConfigPath = "routing.json"

// applyState хранит состояние последнего применения правил
type applyState struct {
	mu      sync.Mutex
	running bool
	lastErr string
	lastPID int
}

// TunHandlers обработчики маршрутизации
type TunHandlers struct {
	server       *Server
	xrayConfig   xray.Config
	proxyManager proxy.Manager
	mu           sync.RWMutex
	routing      *config.RoutingConfig
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
	s.router.HandleFunc("/api/tun/rules/{value}", h.handleDeleteRule).Methods("DELETE", "OPTIONS")
	s.router.HandleFunc("/api/tun/default", h.handleSetDefault).Methods("POST", "OPTIONS")
	s.router.HandleFunc("/api/tun/apply", h.handleApply).Methods("POST", "OPTIONS")
	s.router.HandleFunc("/api/tun/apply/status", h.handleApplyStatus).Methods("GET", "OPTIONS")

	return h
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

	val := strings.TrimSpace(req.Value)
	val = strings.TrimPrefix(val, "https://")
	val = strings.TrimPrefix(val, "http://")
	if idx := strings.Index(val, "/"); idx != -1 {
		val = val[:idx]
	}

	// BUG FIX: пустой val после стриппинга URL (например "https://") приводил к добавлению
	// правила с пустым значением, что генерировало некорректный sing-box конфиг.
	if strings.TrimSpace(val) == "" {
		h.server.respondError(w, http.StatusBadRequest, "пустое значение правила")
		return
	}

	ruleType := config.DetectRuleType(strings.ToLower(val))

	if ruleType != config.RuleTypeProcess {
		val = strings.ToLower(val)
	}

	if req.Action != config.ActionProxy && req.Action != config.ActionDirect && req.Action != config.ActionBlock {
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

	// Валидируем каждое правило
	for i, rule := range req.Rules {
		val := strings.TrimSpace(rule.Value)
		if val == "" {
			h.server.respondError(w, http.StatusBadRequest,
				fmt.Sprintf("правило #%d: пустое значение", i+1))
			return
		}
		if rule.Action != config.ActionProxy && rule.Action != config.ActionDirect && rule.Action != config.ActionBlock {
			h.server.respondError(w, http.StatusBadRequest,
				fmt.Sprintf("правило #%d: неверный action (proxy|direct|block)", i+1))
			return
		}
	}

	// Валидируем default_action (пустая строка → оставляем текущий)
	if req.DefaultAction != "" &&
		req.DefaultAction != config.ActionProxy &&
		req.DefaultAction != config.ActionDirect &&
		req.DefaultAction != config.ActionBlock {
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
	if req.Action != config.ActionProxy && req.Action != config.ActionDirect {
		h.server.respondError(w, http.StatusBadRequest, "action: proxy | direct")
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
	h.apply.mu.Lock()
	if h.apply.running {
		h.apply.mu.Unlock()
		h.server.respondError(w, http.StatusConflict, "применение уже выполняется")
		return
	}
	h.apply.running = true
	h.apply.lastErr = ""
	h.apply.mu.Unlock()

	h.mu.RLock()
	snapshot := &config.RoutingConfig{
		DefaultAction: h.routing.DefaultAction,
		Rules:         make([]config.RoutingRule, len(h.routing.Rules)),
	}
	copy(snapshot.Rules, h.routing.Rules)
	h.mu.RUnlock()

	h.server.respondJSON(w, http.StatusAccepted, map[string]interface{}{
		"message": "применение запущено",
		"rules":   len(snapshot.Rules),
	})

	go h.doApply(snapshot)
}

// doApply выполняет перезапуск sing-box в фоновой горутине
func (h *TunHandlers) doApply(snapshot *config.RoutingConfig) {
	setErr := func(err string) {
		h.apply.mu.Lock()
		h.apply.lastErr = err
		h.apply.mu.Unlock()
	}

	defer func() {
		h.apply.mu.Lock()
		h.apply.running = false
		h.apply.mu.Unlock()
	}()

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

	// Удаляем TUN интерфейс.
	// BUG FIX: exec без таймаута мог зависнуть навсегда если netsh ждал
	// освобождения интерфейса (например при проблемах с правами). Теперь
	// ограничиваем 5 секундами и логируем результат.
	time.Sleep(500 * time.Millisecond)
	if data, err := os.ReadFile(h.xrayConfig.ConfigPath); err == nil {
		var cfg config.SingBoxConfig
		if json.Unmarshal(data, &cfg) == nil {
			for _, inbound := range cfg.Inbounds {
				if inbound.Type == "tun" && inbound.InterfaceName != "" {
					netshCtx, netshCancel := context.WithTimeout(context.Background(), 5*time.Second)
					out, netshErr := exec.CommandContext(netshCtx,
						"C:\\Windows\\System32\\netsh.exe",
						"interface", "delete", "interface", inbound.InterfaceName,
					).CombinedOutput()
					netshCancel()
					if netshErr != nil {
						h.server.logger.Warn("netsh delete interface: %v (output: %s)", netshErr, strings.TrimSpace(string(out)))
					} else {
						h.server.logger.Info("netsh: интерфейс %s удалён", inbound.InterfaceName)
					}
					break
				}
			}
		}
	}

	// Генерируем конфиг
	if err := config.GenerateSingBoxConfig(h.xrayConfig.SecretKeyPath, h.xrayConfig.ConfigPath, snapshot); err != nil {
		h.server.logger.Error("Не удалось сгенерировать конфиг: %v", err)
		setErr(err.Error())
		return
	}

	// Запускаем sing-box
	newManager, err := xray.NewManager(h.xrayConfig)
	if err != nil {
		h.server.logger.Error("Не удалось запустить sing-box: %v", err)
		setErr(err.Error())
		return
	}

	// Ждём готовности sing-box.
	if err := netutil.WaitForPort(DefaultProxyAddress, 15*time.Second); err != nil {
		h.server.logger.Warn("sing-box не ответил за 15с после перезапуска: %v", err)
	}

	// Атомарно заменяем менеджер.
	h.server.configMu.Lock()
	h.server.config.XRayManager = newManager
	h.server.configMu.Unlock()

	h.apply.mu.Lock()
	h.apply.lastPID = newManager.GetPID()
	h.apply.mu.Unlock()

	h.server.logger.Info("Sing-box перезапущен (PID: %d), правил: %d", newManager.GetPID(), len(snapshot.Rules))

	// Всё прошло успешно — восстанавливаем прокси.
	restoreProxy = true
}

// handleApplyStatus GET /api/tun/apply/status — состояние последнего применения
func (h *TunHandlers) handleApplyStatus(w http.ResponseWriter, r *http.Request) {
	h.apply.mu.Lock()
	running := h.apply.running
	lastErr := h.apply.lastErr
	lastPID := h.apply.lastPID
	h.apply.mu.Unlock()

	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"running":  running,
		"last_err": lastErr,
		"last_pid": lastPID,
	})
}
