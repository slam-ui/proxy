package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"proxyclient/internal/apprules"
	"proxyclient/internal/process"

	"github.com/gorilla/mux"
)

// AppProxyHandlers обработчики для per-app proxy
type AppProxyHandlers struct {
	engine   apprules.Engine
	monitor  process.Monitor
	launcher process.Launcher
	server   *Server
}

// SetupAppProxyRoutes настраивает маршруты для per-app proxy
func (s *Server) SetupAppProxyRoutes(engine apprules.Engine, monitor process.Monitor, launcher process.Launcher) {
	handlers := &AppProxyHandlers{
		engine:   engine,
		monitor:  monitor,
		launcher: launcher,
		server:   s,
	}

	// Rules management
	s.router.HandleFunc("/api/apps/rules", handlers.handleListRules).Methods("GET")
	s.router.HandleFunc("/api/apps/rules", handlers.handleCreateRule).Methods("POST")
	s.router.HandleFunc("/api/apps/rules/{id}", handlers.handleGetRule).Methods("GET")
	s.router.HandleFunc("/api/apps/rules/{id}", handlers.handleUpdateRule).Methods("PUT")
	s.router.HandleFunc("/api/apps/rules/{id}", handlers.handleDeleteRule).Methods("DELETE")
	s.router.HandleFunc("/api/apps/rules/{id}/enable", handlers.handleEnableRule).Methods("POST")
	s.router.HandleFunc("/api/apps/rules/{id}/disable", handlers.handleDisableRule).Methods("POST")

	// Process management
	s.router.HandleFunc("/api/apps/processes", handlers.handleListProcesses).Methods("GET")
	s.router.HandleFunc("/api/apps/processes/{pid}", handlers.handleGetProcess).Methods("GET")
	s.router.HandleFunc("/api/apps/processes/refresh", handlers.handleRefreshProcesses).Methods("POST")

	// Launch process
	s.router.HandleFunc("/api/apps/launch", handlers.handleLaunch).Methods("POST")

	// Find matching rule
	s.router.HandleFunc("/api/apps/match", handlers.handleMatchRule).Methods("POST")
}

// Requests and Responses

type CreateRuleRequest struct {
	Name      string          `json:"name"`
	Pattern   string          `json:"pattern"`
	Action    apprules.Action `json:"action"`
	ProxyAddr string          `json:"proxy_addr,omitempty"`
	Priority  int             `json:"priority"`
	Enabled   bool            `json:"enabled"`
}

type LaunchRequest struct {
	Executable string   `json:"executable"`
	RuleID     string   `json:"rule_id,omitempty"`
	Args       []string `json:"args,omitempty"`
}

type LaunchResponse struct {
	PID        int             `json:"pid"`
	Executable string          `json:"executable"`
	RuleID     string          `json:"rule_id,omitempty"`
	ProxyAddr  string          `json:"proxy_addr,omitempty"`
	Action     apprules.Action `json:"action"`
}

type MatchRequest struct {
	ProcessPath string `json:"process_path"`
}

type MatchResponse struct {
	Matched bool           `json:"matched"`
	Rule    *apprules.Rule `json:"rule,omitempty"`
}

// Rules handlers

func (h *AppProxyHandlers) handleListRules(w http.ResponseWriter, r *http.Request) {
	rules := h.engine.ListRules()
	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"rules": rules,
	})
}

func (h *AppProxyHandlers) handleCreateRule(w http.ResponseWriter, r *http.Request) {
	var req CreateRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.server.respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	rule := apprules.Rule{
		Name:      req.Name,
		Pattern:   req.Pattern,
		Action:    req.Action,
		ProxyAddr: req.ProxyAddr,
		Priority:  req.Priority,
		Enabled:   req.Enabled,
	}

	created, err := h.engine.AddRule(rule)
	if err != nil {
		h.server.respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	h.server.respondJSON(w, http.StatusCreated, created)
}

func (h *AppProxyHandlers) handleGetRule(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	rule, err := h.engine.GetRule(id)
	if err != nil {
		h.server.respondError(w, http.StatusNotFound, err.Error())
		return
	}

	h.server.respondJSON(w, http.StatusOK, rule)
}

func (h *AppProxyHandlers) handleUpdateRule(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	var req CreateRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.server.respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	rule := apprules.Rule{
		Name:      req.Name,
		Pattern:   req.Pattern,
		Action:    req.Action,
		ProxyAddr: req.ProxyAddr,
		Priority:  req.Priority,
		Enabled:   req.Enabled,
	}

	updated, err := h.engine.UpdateRule(id, rule)
	if err != nil {
		h.server.respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	h.server.respondJSON(w, http.StatusOK, updated)
}

func (h *AppProxyHandlers) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	if err := h.engine.DeleteRule(id); err != nil {
		h.server.respondError(w, http.StatusNotFound, err.Error())
		return
	}

	h.server.respondJSON(w, http.StatusOK, MessageResponse{
		Message: "Rule deleted successfully",
	})
}

func (h *AppProxyHandlers) handleEnableRule(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	if err := h.engine.EnableRule(id); err != nil {
		h.server.respondError(w, http.StatusNotFound, err.Error())
		return
	}

	h.server.respondJSON(w, http.StatusOK, MessageResponse{
		Message: "Rule enabled successfully",
	})
}

func (h *AppProxyHandlers) handleDisableRule(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	if err := h.engine.DisableRule(id); err != nil {
		h.server.respondError(w, http.StatusNotFound, err.Error())
		return
	}

	h.server.respondJSON(w, http.StatusOK, MessageResponse{
		Message: "Rule disabled successfully",
	})
}

// Process handlers

func (h *AppProxyHandlers) handleListProcesses(w http.ResponseWriter, r *http.Request) {
	processes := h.monitor.GetProcesses()

	// Обогащаем данные о процессах matching rules
	for i := range processes {
		match := h.engine.FindMatchingRule(processes[i].Executable)
		if match.Matched {
			processes[i].RuleID = match.Rule.ID
			processes[i].ProxyStatus = string(match.Rule.Action)
		}
	}

	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"processes": processes,
		"count":     len(processes),
	})
}

func (h *AppProxyHandlers) handleGetProcess(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	var pid int
	if _, err := fmt.Sscanf(vars["pid"], "%d", &pid); err != nil {
		h.server.respondError(w, http.StatusBadRequest, "Invalid PID")
		return
	}

	proc, err := h.monitor.GetProcess(pid)
	if err != nil {
		h.server.respondError(w, http.StatusNotFound, err.Error())
		return
	}

	// Проверяем matching rule
	match := h.engine.FindMatchingRule(proc.Executable)
	if match.Matched {
		proc.RuleID = match.Rule.ID
		proc.ProxyStatus = string(match.Rule.Action)
	}

	h.server.respondJSON(w, http.StatusOK, proc)
}

func (h *AppProxyHandlers) handleRefreshProcesses(w http.ResponseWriter, r *http.Request) {
	if err := h.monitor.Refresh(); err != nil {
		h.server.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	processes := h.monitor.GetProcesses()
	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"message": "Processes refreshed",
		"count":   len(processes),
	})
}

// Launch handler

func (h *AppProxyHandlers) handleLaunch(w http.ResponseWriter, r *http.Request) {
	var req LaunchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.server.respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	result, err := h.launcher.LaunchWithRule(req.Executable, req.RuleID, req.Args...)
	if err != nil {
		h.server.respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	response := LaunchResponse{
		PID:        result.PID,
		Executable: result.Executable,
		RuleID:     result.RuleID,
		ProxyAddr:  result.ProxyAddr,
		Action:     result.Action,
	}

	h.server.respondJSON(w, http.StatusCreated, response)
}

// Match handler

func (h *AppProxyHandlers) handleMatchRule(w http.ResponseWriter, r *http.Request) {
	var req MatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.server.respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	match := h.engine.FindMatchingRule(req.ProcessPath)

	response := MatchResponse{
		Matched: match.Matched,
		Rule:    match.Rule,
	}

	h.server.respondJSON(w, http.StatusOK, response)
}
