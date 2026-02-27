package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"proxyclient/internal/config"
	"proxyclient/internal/xray"

	"github.com/gorilla/mux"
)

// setupTunRoutes регистрирует маршруты для TUN правил
func (s *Server) setupTunRoutes() {
	s.router.HandleFunc("/api/tun/rules", s.handleTunListRules).Methods("GET", "OPTIONS")
	s.router.HandleFunc("/api/tun/rules", s.handleTunAddRule).Methods("POST", "OPTIONS")
	s.router.HandleFunc("/api/tun/rules/{index}", s.handleTunDeleteRule).Methods("DELETE", "OPTIONS")
	s.router.HandleFunc("/api/tun/status", s.handleTunStatus).Methods("GET", "OPTIONS")
	s.router.HandleFunc("/api/tun/enable", s.handleTunEnable).Methods("POST", "OPTIONS")
	s.router.HandleFunc("/api/tun/disable", s.handleTunDisable).Methods("POST", "OPTIONS")
	s.router.HandleFunc("/api/tun/apply", s.handleTunApply).Methods("POST", "OPTIONS")
}

// TunStatusResponse статус TUN режима
type TunStatusResponse struct {
	Enabled      bool                 `json:"enabled"`
	RulesCount   int                  `json:"rules_count"`
	ProcessRules []config.ProcessRule `json:"process_rules"`
}

// AddProcessRuleRequest запрос на добавление правила
type AddProcessRuleRequest struct {
	ProcessName string               `json:"process_name"`
	Action      config.ProcessAction `json:"action"`
}

// handleTunStatus GET /api/tun/status
func (s *Server) handleTunStatus(w http.ResponseWriter, r *http.Request) {
	cfg := s.config.TunConfig
	if cfg == nil {
		cfg = &config.TunConfig{Enabled: false}
	}
	s.respondJSON(w, http.StatusOK, TunStatusResponse{
		Enabled:      cfg.Enabled,
		RulesCount:   len(cfg.ProcessRules),
		ProcessRules: cfg.ProcessRules,
	})
}

// handleTunListRules GET /api/tun/rules
func (s *Server) handleTunListRules(w http.ResponseWriter, r *http.Request) {
	cfg := s.config.TunConfig
	if cfg == nil {
		s.respondJSON(w, http.StatusOK, map[string]interface{}{
			"rules":   []config.ProcessRule{},
			"enabled": false,
		})
		return
	}
	rules := cfg.ProcessRules
	if rules == nil {
		rules = []config.ProcessRule{}
	}
	s.respondJSON(w, http.StatusOK, map[string]interface{}{
		"rules":   rules,
		"enabled": cfg.Enabled,
	})
}

// handleTunAddRule POST /api/tun/rules
func (s *Server) handleTunAddRule(w http.ResponseWriter, r *http.Request) {
	var req AddProcessRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.respondError(w, http.StatusBadRequest, "неверный формат запроса")
		return
	}
	if req.ProcessName == "" {
		s.respondError(w, http.StatusBadRequest, "process_name обязателен")
		return
	}
	if req.Action != config.ProcessProxy && req.Action != config.ProcessDirect && req.Action != config.ProcessBlock {
		s.respondError(w, http.StatusBadRequest, "action должен быть: proxy, direct или block")
		return
	}
	cfg := s.config.TunConfig
	if cfg == nil {
		cfg = &config.TunConfig{Enabled: false}
		s.config.TunConfig = cfg
	}
	for _, rule := range cfg.ProcessRules {
		if rule.ProcessName == req.ProcessName {
			s.respondError(w, http.StatusBadRequest, "правило для этого процесса уже существует")
			return
		}
	}
	newRule := config.ProcessRule{
		ProcessName: req.ProcessName,
		Action:      req.Action,
	}
	cfg.ProcessRules = append(cfg.ProcessRules, newRule)
	if err := config.SaveTunConfig(s.config.TunConfigPath, cfg); err != nil {
		s.logger.Error("Не удалось сохранить tun config: %v", err)
		s.respondError(w, http.StatusInternalServerError, "не удалось сохранить правило")
		return
	}
	s.respondJSON(w, http.StatusCreated, map[string]interface{}{
		"message": "правило добавлено",
		"rule":    newRule,
		"note":    "нажмите Apply чтобы применить изменения",
	})
}

// handleTunDeleteRule DELETE /api/tun/rules/{index}
func (s *Server) handleTunDeleteRule(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	var index int
	if _, err := fmt.Sscanf(vars["index"], "%d", &index); err != nil {
		s.respondError(w, http.StatusBadRequest, "неверный индекс")
		return
	}
	cfg := s.config.TunConfig
	if cfg == nil || index < 0 || index >= len(cfg.ProcessRules) {
		s.respondError(w, http.StatusNotFound, "правило не найдено")
		return
	}
	deleted := cfg.ProcessRules[index]
	cfg.ProcessRules = append(cfg.ProcessRules[:index], cfg.ProcessRules[index+1:]...)
	if err := config.SaveTunConfig(s.config.TunConfigPath, cfg); err != nil {
		s.logger.Error("Не удалось сохранить tun config: %v", err)
		s.respondError(w, http.StatusInternalServerError, "не удалось сохранить изменения")
		return
	}
	s.respondJSON(w, http.StatusOK, map[string]interface{}{
		"message": "правило удалено",
		"deleted": deleted,
		"note":    "нажмите Apply чтобы применить изменения",
	})
}

// handleTunEnable POST /api/tun/enable
func (s *Server) handleTunEnable(w http.ResponseWriter, r *http.Request) {
	cfg := s.config.TunConfig
	if cfg == nil {
		cfg = &config.TunConfig{}
		s.config.TunConfig = cfg
	}
	cfg.Enabled = true
	if err := config.SaveTunConfig(s.config.TunConfigPath, cfg); err != nil {
		s.respondError(w, http.StatusInternalServerError, "не удалось сохранить конфиг")
		return
	}
	s.respondJSON(w, http.StatusOK, MessageResponse{
		Message: "TUN режим включён. Нажмите Apply для применения.",
		Success: true,
	})
}

// handleTunDisable POST /api/tun/disable
func (s *Server) handleTunDisable(w http.ResponseWriter, r *http.Request) {
	cfg := s.config.TunConfig
	if cfg == nil {
		cfg = &config.TunConfig{}
		s.config.TunConfig = cfg
	}
	cfg.Enabled = false
	if err := config.SaveTunConfig(s.config.TunConfigPath, cfg); err != nil {
		s.respondError(w, http.StatusInternalServerError, "не удалось сохранить конфиг")
		return
	}
	s.respondJSON(w, http.StatusOK, MessageResponse{
		Message: "TUN режим отключён. Нажмите Apply для применения.",
		Success: true,
	})
}

// handleTunApply POST /api/tun/apply
func (s *Server) handleTunApply(w http.ResponseWriter, r *http.Request) {
	cfg := s.config.TunConfig
	if err := config.GenerateRuntimeConfigWithTun(
		s.config.TemplatePath,
		s.config.SecretPath,
		s.config.RuntimePath,
		cfg,
	); err != nil {
		s.logger.Error("Не удалось перегенерировать конфиг: %v", err)
		s.respondError(w, http.StatusInternalServerError, "не удалось применить правила: "+err.Error())
		return
	}
	if err := s.config.XRayManager.Stop(); err != nil {
		s.logger.Warn("Ошибка при остановке XRay: %v", err)
	}
	newXray, err := xray.NewManager(xray.Config{
		ExecutablePath: s.config.XRayExecutable,
		ConfigPath:     s.config.RuntimePath,
		Logger:         s.logger,
	})
	if err != nil {
		s.respondError(w, http.StatusInternalServerError, "не удалось перезапустить XRay: "+err.Error())
		return
	}
	s.config.XRayManager = newXray
	s.logger.Info("XRay перезапущен с новыми правилами")
	rulesCount := 0
	if cfg != nil {
		rulesCount = len(cfg.ProcessRules)
	}
	s.respondJSON(w, http.StatusOK, map[string]interface{}{
		"message":     "правила применены, XRay перезапущен",
		"success":     true,
		"rules_count": rulesCount,
	})
}
