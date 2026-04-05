package api

import (
	"encoding/json"
	"net/http"
	"sync"

	"proxyclient/internal/engine"
)

// handleEngineVersion GET /api/engine/version — возвращает установленную и последнюю версии sing-box.
func (s *Server) handleEngineVersion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExecPath string `json:"exec_path"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.ExecPath == "" {
		req.ExecPath = "./sing-box.exe"
	}

	installed := engine.InstalledVersion(req.ExecPath)

	// Получаем последнюю версию из GitHub (с таймаутом из контекста запроса)
	latest, err := engine.LatestVersion(r.Context())
	latestStr := latest
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}

	s.respondJSON(w, http.StatusOK, map[string]interface{}{
		"installed":        installed,
		"latest":           latestStr,
		"update_available": installed != "" && latestStr != "" && installed != latestStr,
		"error":            errStr,
	})
}

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
	api.HandleFunc("/engine/version", s.handleEngineVersion).Methods("GET", "OPTIONS")
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
	// BUG FIX #NEW-2: используем lifecycleCtx вместо context.Background().
	// EnsureEngine выполняет HTTP-запрос с таймаутом 120с. Без lifecycle context
	// при Shutdown приложения горутина продолжала работать до 2 минут, блокируя
	// завершение процесса. lifecycleCtx отменяется при вызове Shutdown() и прерывает
	// HTTP-соединение немедленно — аналогично singbox-launcher который передаёт ctx в download.
	downloadCtx := s.lifecycleCtx
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
		_ = engine.EnsureEngine(downloadCtx, execPath, progress)
	}()

	s.respondJSON(w, http.StatusOK, MessageResponse{Success: true, Message: "загрузка начата"})
}
