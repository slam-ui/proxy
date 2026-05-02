package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"proxyclient/internal/apprules"
	"proxyclient/internal/process"

	"github.com/gorilla/mux"
)

const maxAppProxyRequestBytes = 64 << 10

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
	api := s.router.PathPrefix("/api").Subrouter()

	// Rules management
	api.HandleFunc("/apps/rules", handlers.handleListRules).Methods("GET", "OPTIONS")
	api.HandleFunc("/apps/rules", handlers.handleCreateRule).Methods("POST", "OPTIONS")
	api.HandleFunc("/apps/rules/{id}", handlers.handleGetRule).Methods("GET", "OPTIONS")
	api.HandleFunc("/apps/rules/{id}", handlers.handleUpdateRule).Methods("PUT", "OPTIONS")
	api.HandleFunc("/apps/rules/{id}", handlers.handleDeleteRule).Methods("DELETE", "OPTIONS")
	api.HandleFunc("/apps/rules/{id}/enable", handlers.handleEnableRule).Methods("POST", "OPTIONS")
	api.HandleFunc("/apps/rules/{id}/disable", handlers.handleDisableRule).Methods("POST", "OPTIONS")

	// Process management
	api.HandleFunc("/apps/processes", handlers.handleListProcesses).Methods("GET", "OPTIONS")
	// {pid:[0-9]+} ограничивает паттерн только числами.
	// Без этого GET /api/apps/processes/refresh попадал бы в этот хендлер
	// вместо 405 Method Not Allowed, и возвращал 400 "Invalid PID".
	api.HandleFunc("/apps/processes/{pid:[0-9]+}", handlers.handleGetProcess).Methods("GET", "OPTIONS")
	api.HandleFunc("/apps/processes/refresh", handlers.handleRefreshProcesses).Methods("POST", "OPTIONS")

	// Launch process
	api.HandleFunc("/apps/launch", handlers.handleLaunch).Methods("POST", "OPTIONS")

	// Find matching rule
	api.HandleFunc("/apps/match", handlers.handleMatchRule).Methods("POST", "OPTIONS")
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
	r.Body = http.MaxBytesReader(w, r.Body, maxAppProxyRequestBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		h.server.respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	var extra struct{}
	if err := dec.Decode(&extra); err == nil {
		h.server.respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	} else if !errors.Is(err, io.EOF) {
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
	r.Body = http.MaxBytesReader(w, r.Body, maxAppProxyRequestBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		h.server.respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	var extra struct{}
	if err := dec.Decode(&extra); err == nil {
		h.server.respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	} else if !errors.Is(err, io.EOF) {
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
	// OPT #2: ProxyStatus и RuleID уже закэшированы в ProcessInfo во время refresh().
	// Цикл FindMatchingRule удалён — данные готовы без дополнительных вычислений.
	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"processes": processes,
		"count":     len(processes),
	})
}

func (h *AppProxyHandlers) handleGetProcess(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	// strconv.Atoi в отличие от fmt.Sscanf возвращает ошибку при любом
	// нечисловом символе: "123abc" у Sscanf вернул бы pid=123 без ошибки.
	pid, err := strconv.Atoi(vars["pid"])
	if err != nil {
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
	r.Body = http.MaxBytesReader(w, r.Body, maxAppProxyRequestBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		h.server.respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	var extra struct{}
	if err := dec.Decode(&extra); err == nil {
		h.server.respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	} else if !errors.Is(err, io.EOF) {
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
	r.Body = http.MaxBytesReader(w, r.Body, maxAppProxyRequestBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		h.server.respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	var extra struct{}
	if err := dec.Decode(&extra); err == nil {
		h.server.respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	} else if !errors.Is(err, io.EOF) {
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
