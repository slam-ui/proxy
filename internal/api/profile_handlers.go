package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"proxyclient/internal/config"

	"github.com/gorilla/mux"
)

const profilesDir = "profiles"

// Profile сохранённый набор правил маршрутизации с именем
type Profile struct {
	Name      string              `json:"name"`
	CreatedAt time.Time           `json:"created_at"`
	UpdatedAt time.Time           `json:"updated_at"`
	Routing   config.RoutingConfig `json:"routing"`
}

// ProfileHandlers обработчики для профилей правил
type ProfileHandlers struct {
	server *Server
	mu     sync.RWMutex
}

var reValidName = regexp.MustCompile(`^[\p{L}\p{N} _-]{1,64}$`)  // letters, digits, space, _, -

func SetupProfileRoutes(s *Server) {
	h := &ProfileHandlers{server: s}
	// Создаём директорию profiles если не существует
	_ = os.MkdirAll(profilesDir, 0755)

	s.router.HandleFunc("/api/profiles", h.handleList).Methods("GET", "OPTIONS")
	s.router.HandleFunc("/api/profiles", h.handleSave).Methods("POST", "OPTIONS")
	s.router.HandleFunc("/api/profiles/{name}", h.handleLoad).Methods("GET", "OPTIONS")
	s.router.HandleFunc("/api/profiles/{name}", h.handleDelete).Methods("DELETE", "OPTIONS")
}

// handleList GET /api/profiles — список всех сохранённых профилей
func (h *ProfileHandlers) handleList(w http.ResponseWriter, _ *http.Request) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	entries, err := os.ReadDir(profilesDir)
	if err != nil {
		h.server.respondJSON(w, http.StatusOK, map[string]interface{}{"profiles": []interface{}{}})
		return
	}

	var profiles []map[string]interface{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p, err := loadProfile(e.Name())
		if err != nil {
			continue
		}
		profiles = append(profiles, map[string]interface{}{
			"name":       p.Name,
			"created_at": p.CreatedAt,
			"updated_at": p.UpdatedAt,
			"rule_count": len(p.Routing.Rules),
		})
	}
	if profiles == nil {
		profiles = []map[string]interface{}{}
	}
	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{"profiles": profiles})
}

// handleSave POST /api/profiles — сохраняет текущие правила как именованный профиль
// Body: {"name": "Работа", "routing": {...}}
func (h *ProfileHandlers) handleSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string               `json:"name"`
		Routing config.RoutingConfig `json:"routing"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.server.respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Name == "" {
		h.server.respondError(w, http.StatusBadRequest, "имя профиля не может быть пустым")
		return
	}
	if !reValidName.MatchString(req.Name) {
		h.server.respondError(w, http.StatusBadRequest,
			"имя профиля должно быть 1–64 символа (буквы, цифры, пробел, _ -)")
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	filename := sanitizeFilename(req.Name) + ".json"
	existing, _ := loadProfile(filename)

	now := time.Now()
	p := Profile{
		Name:      req.Name,
		CreatedAt: now,
		UpdatedAt: now,
		Routing:   req.Routing,
	}
	if existing != nil {
		p.CreatedAt = existing.CreatedAt
	}

	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		h.server.respondError(w, http.StatusInternalServerError, "marshal error")
		return
	}
	path := filepath.Join(profilesDir, filename)
	if err := os.WriteFile(path, data, 0644); err != nil {
		h.server.respondError(w, http.StatusInternalServerError, "write error: "+err.Error())
		return
	}
	h.server.respondJSON(w, http.StatusOK, MessageResponse{
		Success: true,
		Message: fmt.Sprintf("профиль %q сохранён (%d правил)", req.Name, len(req.Routing.Rules)),
	})
}

// handleLoad GET /api/profiles/{name} — возвращает профиль по имени
func (h *ProfileHandlers) handleLoad(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	if !reValidName.MatchString(name) {
		h.server.respondError(w, http.StatusBadRequest, "недопустимое имя профиля")
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()

	p, err := loadProfile(sanitizeFilename(name) + ".json")
	if err != nil {
		h.server.respondError(w, http.StatusNotFound, "профиль не найден")
		return
	}
	h.server.respondJSON(w, http.StatusOK, p)
}

// handleDelete DELETE /api/profiles/{name} — удаляет профиль
func (h *ProfileHandlers) handleDelete(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	if !reValidName.MatchString(name) {
		h.server.respondError(w, http.StatusBadRequest, "недопустимое имя профиля")
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	path := filepath.Join(profilesDir, sanitizeFilename(name)+".json")
	if err := os.Remove(path); err != nil {
		h.server.respondError(w, http.StatusNotFound, "профиль не найден")
		return
	}
	h.server.respondJSON(w, http.StatusOK, MessageResponse{Success: true, Message: "профиль удалён"})
}

func loadProfile(filename string) (*Profile, error) {
	data, err := os.ReadFile(filepath.Join(profilesDir, filename))
	if err != nil {
		return nil, err
	}
	var p Profile
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// sanitizeFilename убирает из имени символы опасные для файловой системы
func sanitizeFilename(name string) string {
	r := strings.NewReplacer(
		"/", "_", "\\", "_", ":", "_", "*", "_",
		"?", "_", "\"", "_", "<", "_", ">", "_", "|", "_",
		" ", "_",
	)
	return r.Replace(name)
}
