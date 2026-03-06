package api

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"proxyclient/internal/config"
	"proxyclient/internal/xray"
)

const routingConfigPath = "routing.json"
const singboxConfigPath = "config.singbox.json"

// TunHandlers обработчики маршрутизации
type TunHandlers struct {
	server     *Server
	xrayConfig xray.Config
	mu         sync.RWMutex
	routing    *config.RoutingConfig
}

// SetupTunRoutes регистрирует маршруты
func (s *Server) SetupTunRoutes(xrayCfg xray.Config) *TunHandlers {
	routing, err := config.LoadRoutingConfig(routingConfigPath)
	if err != nil {
		s.logger.Warn("Не удалось загрузить routing config: %v", err)
		routing = config.DefaultRoutingConfig()
	}

	h := &TunHandlers{
		server:     s,
		xrayConfig: xrayCfg,
		routing:    routing,
	}

	s.router.HandleFunc("/api/tun/rules", h.handleListRules).Methods("GET", "OPTIONS")
	s.router.HandleFunc("/api/tun/rules", h.handleAddRule).Methods("POST", "OPTIONS")
	s.router.HandleFunc("/api/tun/rules/{value}", h.handleDeleteRule).Methods("DELETE", "OPTIONS")
	s.router.HandleFunc("/api/tun/default", h.handleSetDefault).Methods("POST", "OPTIONS")
	s.router.HandleFunc("/api/tun/apply", h.handleApply).Methods("POST", "OPTIONS")

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

	// Нормализация значения
	val := strings.TrimSpace(req.Value)
	val = strings.TrimPrefix(val, "https://")
	val = strings.TrimPrefix(val, "http://")
	if idx := strings.Index(val, "/"); idx != -1 {
		val = val[:idx]
	}
	// Определяем тип ДО нормализации регистра
	ruleType := config.DetectRuleType(strings.ToLower(val))

	// Для процессов сохраняем оригинальный регистр, для доменов — нижний
	if ruleType != config.RuleTypeProcess {
		val = strings.ToLower(val)
	}

	if req.Action != config.ActionProxy && req.Action != config.ActionDirect && req.Action != config.ActionBlock {
		h.server.respondError(w, http.StatusBadRequest, "action: proxy | direct | block")
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	// Проверяем дубликат
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

	if err := config.SaveRoutingConfig(routingConfigPath, h.routing); err != nil {
		h.server.logger.Warn("Не удалось сохранить routing config: %v", err)
	}

	h.server.respondJSON(w, http.StatusCreated, map[string]interface{}{
		"message": "правило добавлено",
		"rule":    newRule,
	})
}

// handleDeleteRule DELETE /api/tun/rules/{value}
func (h *TunHandlers) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(r.URL.Path, "/")
	value := strings.ToLower(parts[len(parts)-1])

	h.mu.Lock()
	defer h.mu.Unlock()

	newRules := h.routing.Rules[:0]
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

	h.routing.Rules = newRules
	if err := config.SaveRoutingConfig(routingConfigPath, h.routing); err != nil {
		h.server.logger.Warn("Не удалось сохранить routing config: %v", err)
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
	h.routing.DefaultAction = req.Action
	if err := config.SaveRoutingConfig(routingConfigPath, h.routing); err != nil {
		h.server.logger.Warn("Не удалось сохранить routing config: %v", err)
	}
	h.mu.Unlock()

	h.server.respondJSON(w, http.StatusOK, MessageResponse{Message: "дефолтное действие обновлено", Success: true})
}

// handleApply POST /api/tun/apply — генерирует sing-box конфиг и перезапускает
func (h *TunHandlers) handleApply(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	snapshot := &config.RoutingConfig{
		DefaultAction: h.routing.DefaultAction,
		Rules:         make([]config.RoutingRule, len(h.routing.Rules)),
	}
	copy(snapshot.Rules, h.routing.Rules)
	h.mu.RUnlock()

	// Останавливаем текущий процесс
	if h.server.config.XRayManager != nil {
		if err := h.server.config.XRayManager.Stop(); err != nil {
			h.server.logger.Warn("Ошибка при остановке: %v", err)
		}
	}
	// Останавливаем текущий процесс
	if h.server.config.XRayManager != nil {
		if err := h.server.config.XRayManager.Stop(); err != nil {
			h.server.logger.Warn("Ошибка при остановке: %v", err)
		}
	}

	// Удаляем TUN интерфейс через netsh с полным путём
	// Удаляем TUN интерфейс — читаем актуальное имя из конфига
	time.Sleep(500 * time.Millisecond)
	if data, err := os.ReadFile(h.xrayConfig.ConfigPath); err == nil {
		var cfg config.SingBoxConfig
		if json.Unmarshal(data, &cfg) == nil {
			for _, inbound := range cfg.Inbounds {
				if inbound.Type == "tun" && inbound.InterfaceName != "" {
					exec.Command("C:\\Windows\\System32\\netsh.exe",
						"interface", "delete", "interface", inbound.InterfaceName).Run()
					break
				}
			}
		}
	}
	time.Sleep(2 * time.Second)

	// Генерируем sing-box конфиг
	if err := config.GenerateSingBoxConfig("secret.key", h.xrayConfig.ConfigPath, snapshot); err != nil {
		h.server.respondError(w, http.StatusInternalServerError,
			"не удалось сгенерировать конфиг: "+err.Error())
		return
	}

	// Запускаем sing-box
	newManager, err := xray.NewManager(h.xrayConfig)
	if err != nil {
		h.server.respondError(w, http.StatusInternalServerError,
			"не удалось запустить sing-box: "+err.Error())
		return
	}

	h.server.config.XRayManager = newManager
	h.server.logger.Info("Sing-box перезапущен (PID: %d), правил: %d", newManager.GetPID(), len(snapshot.Rules))

	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"message": "правила применены, sing-box перезапущен",
		"pid":     newManager.GetPID(),
		"rules":   len(snapshot.Rules),
	})
}
