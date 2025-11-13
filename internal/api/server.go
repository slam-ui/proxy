package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	_ "os"
	"proxyclient/internal/xray"

	"github.com/gorilla/mux"
)

// Server — HTTP API сервер
type Server struct {
	xrayManager *xray.Manager
	configPath  string
}

// NewServer создаёт новый API сервер
func NewServer(xrayManager *xray.Manager, configPath string) *Server {
	return &Server{
		xrayManager: xrayManager,
		configPath:  configPath,
	}
}

// Start запускает HTTP сервер
func (s *Server) Start(addr string) error {
	r := mux.NewRouter()

	r.HandleFunc("/api/status", s.handleStatus).Methods("GET")
	r.HandleFunc("/api/start", s.handleStart).Methods("POST")
	r.HandleFunc("/api/stop", s.handleStop).Methods("POST")
	r.HandleFunc("/api/restart", s.handleRestart).Methods("POST")

	fmt.Printf("🌐 API запущен на %s\n", addr)
	return http.ListenAndServe(addr, r)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"running": s.xrayManager != nil,
		"pid":     s.xrayManager.Cmd.Process.Pid,
		"config":  s.configPath,
	})
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	if s.xrayManager != nil {
		http.Error(w, "XRay уже запущен", http.StatusBadRequest)
		return
	}

	// Перезапуск конфига (если нужно)
	// ...

	// Запуск
	manager, err := xray.NewManager("./xray_core/xray.exe", s.configPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.xrayManager = manager
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if s.xrayManager == nil {
		http.Error(w, "XRay не запущен", http.StatusBadRequest)
		return
	}

	err := s.xrayManager.Stop()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.xrayManager = nil
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	s.handleStop(w, r)
	if w.Header().Get("Content-Type") == "application/json" { // если предыдущий запрос прошёл успешно
		s.handleStart(w, r)
	}
}
