package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"

	"proxyclient/internal/engine"
)

// engineState хранит состояние загрузки движка
type engineState struct {
	mu       sync.RWMutex
	running  bool
	progress engine.Progress
}

var globalEngine engineState

// SetupEngineRoutes регистрирует маршруты для управления движком
func SetupEngineRoutes(s *Server) {
	api := s.router.PathPrefix("/api").Subrouter()
	api.HandleFunc("/engine/status", s.handleEngineStatus).Methods("GET", "OPTIONS")
	api.HandleFunc("/engine/download", s.handleEngineDownload).Methods("POST", "OPTIONS")
}

// handleEngineStatus GET /api/engine/status
func (s *Server) handleEngineStatus(w http.ResponseWriter, _ *http.Request) {
	globalEngine.mu.RLock()
	defer globalEngine.mu.RUnlock()
	errStr := ""
	if globalEngine.progress.Err != nil {
		errStr = globalEngine.progress.Err.Error()
	}
	s.respondJSON(w, http.StatusOK, map[string]interface{}{
		"running": globalEngine.running,
		"stage":   globalEngine.progress.Stage,
		"message": globalEngine.progress.Message,
		"percent": globalEngine.progress.Percent,
		"version": globalEngine.progress.Version,
		"error":   errStr,
	})
}

// handleEngineDownload POST /api/engine/download — запускает загрузку sing-box.exe
func (s *Server) handleEngineDownload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExecPath string `json:"exec_path"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.ExecPath == "" {
		req.ExecPath = "./sing-box.exe"
	}

	globalEngine.mu.Lock()
	if globalEngine.running {
		globalEngine.mu.Unlock()
		s.respondError(w, http.StatusConflict, "загрузка уже выполняется")
		return
	}
	globalEngine.running = true
	globalEngine.progress = engine.Progress{Stage: "starting", Message: "Инициализация...", Percent: 0}
	globalEngine.mu.Unlock()

	execPath := req.ExecPath
	progress := make(chan engine.Progress, 20)
	go func() {
		defer func() {
			globalEngine.mu.Lock()
			globalEngine.running = false
			globalEngine.mu.Unlock()
			close(progress)
		}()
		go func() {
			for p := range progress {
				globalEngine.mu.Lock()
				globalEngine.progress = p
				globalEngine.mu.Unlock()
			}
		}()
		_ = engine.EnsureEngine(context.Background(), execPath, progress)
	}()

	s.respondJSON(w, http.StatusOK, MessageResponse{Success: true, Message: "загрузка начата"})
}
