package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"proxyclient/internal/config"
	"proxyclient/internal/fileutil"

	"github.com/gorilla/mux"
)

const profilesDir = "profiles"

var reValidName = regexp.MustCompile(`^[\p{L}\p{N} _-]{1,64}$`) // letters, digits, space, _, -

// Profile сохранённый набор правил маршрутизации с именем
type Profile struct {
	Name      string               `json:"name"`
	CreatedAt time.Time            `json:"created_at"`
	UpdatedAt time.Time            `json:"updated_at"`
	Routing   config.RoutingConfig `json:"routing"`
}

// profileMeta — лёгкие метаданные профиля для кэша (не хранит полный Routing).
type profileMeta struct {
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
	RuleCount int
	mtime     time.Time // mtime файла на момент последнего чтения
}

// ProfileHandlers обработчики для профилей правил
type ProfileHandlers struct {
	server *Server
	mu     sync.RWMutex
	// OPT #6: кэш метаданных профилей — инвалидируется только при изменении mtime файла.
	// Ранее handleList читал и парсил полный JSON каждого профиля при каждом запросе UI.
	// Теперь: ReadDir + Stat (дёшево), парсинг JSON только когда файл реально изменился.
	metaCache map[string]profileMeta
}

func SetupProfileRoutes(s *Server) {
	h := &ProfileHandlers{
		server:    s,
		metaCache: make(map[string]profileMeta),
	}
	_ = os.MkdirAll(profilesDir, 0755)

	s.router.HandleFunc("/api/profiles", h.handleList).Methods("GET", "OPTIONS")
	s.router.HandleFunc("/api/profiles", h.handleSave).Methods("POST", "OPTIONS")
	s.router.HandleFunc("/api/profiles/builtins", s.handleBuiltinProfiles).Methods("GET", "OPTIONS")
	s.router.HandleFunc("/api/profiles/{name}/apply", h.handleApply).Methods("POST", "OPTIONS")
	s.router.HandleFunc("/api/profiles/{name}", h.handleLoad).Methods("GET", "OPTIONS")
	s.router.HandleFunc("/api/profiles/{name}", h.handleDelete).Methods("DELETE", "OPTIONS")
}

// handleList GET /api/profiles — список всех сохранённых профилей
func (h *ProfileHandlers) handleList(w http.ResponseWriter, _ *http.Request) {
	// FIX Bug4: используем Lock, а не RLock — функция пишет в h.metaCache.
	// Несколько горутин с RLock одновременно писали в одну map → data race (panic/corruption).
	h.mu.Lock()
	defer h.mu.Unlock()

	entries, err := os.ReadDir(profilesDir)
	if err != nil {
		h.server.respondJSON(w, http.StatusOK, map[string]interface{}{"profiles": []interface{}{}})
		return
	}

	// Убираем из кэша удалённые файлы.
	activeFiles := make(map[string]bool, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			activeFiles[e.Name()] = true
		}
	}
	for name := range h.metaCache {
		if !activeFiles[name] {
			delete(h.metaCache, name)
		}
	}

	var profiles []map[string]interface{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		mtime := fi.ModTime()

		// OPT #6: проверяем кэш — если mtime не изменился, используем кэшированные метаданные.
		if cached, ok := h.metaCache[e.Name()]; ok && cached.mtime.Equal(mtime) {
			profiles = append(profiles, map[string]interface{}{
				"name":       cached.Name,
				"created_at": cached.CreatedAt,
				"updated_at": cached.UpdatedAt,
				"rule_count": cached.RuleCount,
			})
			continue
		}

		// Кэш устарел — читаем файл.
		p, err := loadProfile(e.Name())
		if err != nil {
			continue
		}
		meta := profileMeta{
			Name:      p.Name,
			CreatedAt: p.CreatedAt,
			UpdatedAt: p.UpdatedAt,
			RuleCount: len(p.Routing.Rules),
			mtime:     mtime,
		}
		h.metaCache[e.Name()] = meta
		profiles = append(profiles, map[string]interface{}{
			"name":       meta.Name,
			"created_at": meta.CreatedAt,
			"updated_at": meta.UpdatedAt,
			"rule_count": meta.RuleCount,
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
	// BUG FIX: os.WriteFile не атомарен — при крэше профиль будет повреждён.
	// fileutil.WriteAtomic пишет во временный файл, затем атомарно переименовывает.
	path := filepath.Join(profilesDir, filename)
	if err := fileutil.WriteAtomic(path, data, 0644); err != nil {
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

// handleApply POST /api/profiles/{name}/apply — применяет профиль к текущему routing.
func (h *ProfileHandlers) handleApply(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	if !reValidName.MatchString(name) {
		h.server.respondError(w, http.StatusBadRequest, "недопустимое имя профиля")
		return
	}
	if h.server.tunHandlers == nil {
		h.server.respondError(w, http.StatusServiceUnavailable, "TUN routing не инициализирован")
		return
	}

	h.mu.RLock()
	p, err := loadProfile(sanitizeFilename(name) + ".json")
	h.mu.RUnlock()
	if err != nil {
		h.server.respondError(w, http.StatusNotFound, "профиль не найден")
		return
	}

	count, applyErr, err := h.server.tunHandlers.replaceRoutingAndApply(p.Routing)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errSaveRoutingConfig) {
			status = http.StatusInternalServerError
			h.server.logger.Error("handleApplyProfile: не удалось сохранить routing config: %v", err)
		}
		h.server.respondError(w, status, err.Error())
		return
	}
	if applyErr != "" {
		h.server.logger.Warn("handleApplyProfile: TriggerApply: %v", applyErr)
	}
	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"message":     fmt.Sprintf("профиль %q применён", p.Name),
		"count":       count,
		"apply_error": applyErr,
	})
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
