package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"proxyclient/internal/apprules"
	"proxyclient/internal/config"
	"proxyclient/internal/fileutil"

	"github.com/gorilla/mux"
)

const profilesDir = "profiles"
const maxProfileSaveRequestBytes = 1 << 20

var reValidName = regexp.MustCompile(`^[\p{L}\p{N} _-]{1,64}$`) // letters, digits, space, _, -

type ServerSelector struct {
	Mode     string `json:"mode"` // specific|auto|subscription_auto
	ServerID string `json:"server_id,omitempty"`
	SubID    string `json:"sub_id,omitempty"`
}

type KillSwitchMode string

const (
	KillSwitchOff       KillSwitchMode = "off"
	KillSwitchConnected KillSwitchMode = "connected"
	KillSwitchAlways    KillSwitchMode = "always"
)

// Profile сохранённый набор правил маршрутизации с именем
type Profile struct {
	ID             string               `json:"id"`
	Name           string               `json:"name"`
	Description    string               `json:"description,omitempty"`
	Icon           string               `json:"icon,omitempty"`
	Color          string               `json:"color,omitempty"`
	ServerSelector ServerSelector       `json:"server_selector"`
	AppRules       []apprules.Rule      `json:"app_rules,omitempty"`
	RoutingRules   []config.RoutingRule `json:"routing_rules,omitempty"`
	Routing        config.RoutingConfig `json:"routing"`
	DNSConfig      *config.DNSConfig    `json:"dns_config,omitempty"`
	KillSwitch     KillSwitchMode       `json:"kill_switch,omitempty"`
	SplitTunnel    []string             `json:"split_tunnel,omitempty"`
	AutoConnect    bool                 `json:"auto_connect"`
	Hotkey         string               `json:"hotkey,omitempty"`
	CreatedAt      time.Time            `json:"created_at"`
	UpdatedAt      time.Time            `json:"updated_at"`
}

// profileMeta — лёгкие метаданные профиля для кэша (не хранит полный Routing).
type profileMeta struct {
	ID             string
	Name           string
	Description    string
	Icon           string
	Color          string
	ServerSelector ServerSelector
	AutoConnect    bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
	RuleCount      int
	AppRuleCount   int
	mtime          time.Time // mtime файла на момент последнего чтения
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
				"id":              cached.ID,
				"name":            cached.Name,
				"description":     cached.Description,
				"icon":            cached.Icon,
				"color":           cached.Color,
				"server_selector": cached.ServerSelector,
				"auto_connect":    cached.AutoConnect,
				"created_at":      cached.CreatedAt,
				"updated_at":      cached.UpdatedAt,
				"rule_count":      cached.RuleCount,
				"app_rule_count":  cached.AppRuleCount,
			})
			continue
		}

		// Кэш устарел — читаем файл.
		p, err := loadProfile(e.Name())
		if err != nil {
			continue
		}
		meta := profileMeta{
			ID:             p.ID,
			Name:           p.Name,
			Description:    p.Description,
			Icon:           p.Icon,
			Color:          p.Color,
			ServerSelector: p.ServerSelector,
			AutoConnect:    p.AutoConnect,
			CreatedAt:      p.CreatedAt,
			UpdatedAt:      p.UpdatedAt,
			RuleCount:      len(profileRoutingRules(p)),
			AppRuleCount:   len(p.AppRules),
			mtime:          mtime,
		}
		h.metaCache[e.Name()] = meta
		profiles = append(profiles, map[string]interface{}{
			"id":              meta.ID,
			"name":            meta.Name,
			"description":     meta.Description,
			"icon":            meta.Icon,
			"color":           meta.Color,
			"server_selector": meta.ServerSelector,
			"auto_connect":    meta.AutoConnect,
			"created_at":      meta.CreatedAt,
			"updated_at":      meta.UpdatedAt,
			"rule_count":      meta.RuleCount,
			"app_rule_count":  meta.AppRuleCount,
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
		ID             string               `json:"id"`
		Name           string               `json:"name"`
		Description    string               `json:"description"`
		Icon           string               `json:"icon"`
		Color          string               `json:"color"`
		ServerSelector ServerSelector       `json:"server_selector"`
		AppRules       []apprules.Rule      `json:"app_rules"`
		RoutingRules   []config.RoutingRule `json:"routing_rules"`
		Routing        config.RoutingConfig `json:"routing"`
		DNSConfig      *config.DNSConfig    `json:"dns_config"`
		KillSwitch     KillSwitchMode       `json:"kill_switch"`
		SplitTunnel    []string             `json:"split_tunnel"`
		AutoConnect    bool                 `json:"auto_connect"`
		Hotkey         string               `json:"hotkey"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxProfileSaveRequestBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		h.server.respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	var extra struct{}
	if err := dec.Decode(&extra); err == nil {
		h.server.respondError(w, http.StatusBadRequest, "invalid JSON: multiple JSON values")
		return
	} else if !errors.Is(err, io.EOF) {
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
	if req.Routing.DefaultAction == "" {
		req.Routing.DefaultAction = config.ActionProxy
	}
	if len(req.RoutingRules) > 0 {
		req.Routing.Rules = req.RoutingRules
	}
	if req.DNSConfig != nil {
		req.Routing.DNS = req.DNSConfig
	}
	if !isValidAction(req.Routing.DefaultAction) {
		h.server.respondError(w, http.StatusBadRequest, "default_action: proxy | direct | block")
		return
	}
	if err := normalizeRoutingRules(req.Routing.Rules); err != nil {
		h.server.respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	config.SanitizeRoutingConfig(&req.Routing)
	if err := validateServerSelector(req.ServerSelector); err != nil {
		h.server.respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.KillSwitch == "" {
		req.KillSwitch = KillSwitchConnected
	}
	if !validKillSwitchMode(req.KillSwitch) {
		h.server.respondError(w, http.StatusBadRequest, "kill_switch: off | connected | always")
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	filename := sanitizeFilename(req.Name) + ".json"
	existing, _ := loadProfile(filename)

	now := time.Now()
	p := Profile{
		ID:             firstNonEmptyAPI(req.ID, sanitizeFilename(req.Name)),
		Name:           req.Name,
		Description:    strings.TrimSpace(req.Description),
		Icon:           strings.TrimSpace(req.Icon),
		Color:          strings.TrimSpace(req.Color),
		ServerSelector: req.ServerSelector,
		AppRules:       req.AppRules,
		RoutingRules:   req.Routing.Rules,
		Routing:        req.Routing,
		DNSConfig:      req.Routing.DNS,
		KillSwitch:     req.KillSwitch,
		SplitTunnel:    req.SplitTunnel,
		AutoConnect:    req.AutoConnect,
		Hotkey:         strings.TrimSpace(req.Hotkey),
		CreatedAt:      now,
		UpdatedAt:      now,
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
	path, err := profilePath(filename)
	if err != nil {
		h.server.respondError(w, http.StatusBadRequest, "недопустимое имя профиля")
		return
	}
	if err := fileutil.WriteAtomic(path, data, 0644); err != nil {
		h.server.respondError(w, http.StatusInternalServerError, "write error: "+err.Error())
		return
	}
	h.server.respondJSON(w, http.StatusOK, MessageResponse{
		Success: true,
		Message: fmt.Sprintf("профиль %q сохранён (%d правил)", req.Name, len(p.Routing.Rules)),
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
	appRulesApplied := false
	if len(p.AppRules) > 0 {
		storage := apprules.NewFileStorage(filepath.Join(config.DataDir, "app_rules.json"))
		if err := storage.Save(p.AppRules); err != nil {
			h.server.respondError(w, http.StatusInternalServerError, "app rules: "+err.Error())
			return
		}
		appRulesApplied = true
	}
	connectedID, connectErr := h.applyProfileServerSelector(r.Context(), p)
	if connectErr != nil {
		h.server.respondError(w, http.StatusConflict, connectErr.Error())
		return
	}
	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"message":           fmt.Sprintf("профиль %q применён", p.Name),
		"count":             count,
		"apply_error":       applyErr,
		"app_rules_applied": appRulesApplied,
		"connected_id":      connectedID,
	})
}

func (h *ProfileHandlers) applyProfileServerSelector(ctx context.Context, p *Profile) (string, error) {
	if !p.AutoConnect || h.server.serversHandlers == nil {
		return "", nil
	}
	switch p.ServerSelector.Mode {
	case "", "auto":
		resp, _, err := h.server.serversHandlers.doAutoConnect(ctx)
		if err != nil {
			return "", err
		}
		id, _ := resp["connected_id"].(string)
		return id, nil
	case "specific":
		return h.activateProfileServerByID(p.ServerSelector.ServerID)
	case "subscription_auto":
		return h.activateProfileSubscriptionServer(p.ServerSelector.SubID)
	default:
		return "", fmt.Errorf("unsupported server selector")
	}
}

func (h *ProfileHandlers) activateProfileSubscriptionServer(subID string) (string, error) {
	if subID == "" {
		return h.applyProfileServerSelector(context.Background(), &Profile{AutoConnect: true, ServerSelector: ServerSelector{Mode: "auto"}})
	}
	h.server.serversHandlers.mu.RLock()
	list, err := loadServers()
	h.server.serversHandlers.mu.RUnlock()
	if err != nil {
		return "", err
	}
	for _, server := range visibleServers(list) {
		if server.SubscriptionID == subID {
			return h.activateProfileServer(server)
		}
	}
	return "", fmt.Errorf("subscription server not found")
}

func (h *ProfileHandlers) activateProfileServerByID(id string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("profile server not selected")
	}
	h.server.serversHandlers.mu.RLock()
	list, err := loadServers()
	h.server.serversHandlers.mu.RUnlock()
	if err != nil {
		return "", err
	}
	for _, server := range visibleServers(list) {
		if server.ID == id {
			return h.activateProfileServer(server)
		}
	}
	resp, _, err := h.server.serversHandlers.doAutoConnect(context.Background())
	if err != nil {
		return "", fmt.Errorf("profile server not found and auto fallback failed: %w", err)
	}
	id, _ = resp["connected_id"].(string)
	return id, nil
}

func (h *ProfileHandlers) activateProfileServer(server ServerEntry) (string, error) {
	if err := config.WriteSecretKey(h.server.serversHandlers.secretKey, server.URL); err != nil {
		return "", err
	}
	config.InvalidateVLESSCache()
	if h.server.config.SecretKeyUpdatedFn != nil {
		h.server.config.SecretKeyUpdatedFn()
	}
	if h.server.tunHandlers != nil {
		if err := h.server.tunHandlers.TriggerApplyFull(); err != nil {
			h.server.logger.Warn("profile activate server: TriggerApplyFull: %v", err)
		}
	}
	return server.ID, nil
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

	path, err := profilePath(sanitizeFilename(name) + ".json")
	if err != nil {
		h.server.respondError(w, http.StatusBadRequest, "недопустимое имя профиля")
		return
	}
	if err := os.Remove(path); err != nil {
		h.server.respondError(w, http.StatusNotFound, "профиль не найден")
		return
	}
	h.server.respondJSON(w, http.StatusOK, MessageResponse{Success: true, Message: "профиль удалён"})
}

func loadProfile(filename string) (*Profile, error) {
	path, err := profilePath(filename)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Profile
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	normalizeProfile(&p)
	return &p, nil
}

func normalizeProfile(p *Profile) {
	if p.ID == "" {
		p.ID = sanitizeFilename(p.Name)
	}
	if len(p.RoutingRules) == 0 && len(p.Routing.Rules) > 0 {
		p.RoutingRules = p.Routing.Rules
	}
	if p.Routing.DefaultAction == "" {
		p.Routing.DefaultAction = config.ActionProxy
	}
	if p.Routing.DNS != nil && p.DNSConfig == nil {
		p.DNSConfig = p.Routing.DNS
	}
	if p.KillSwitch == "" {
		p.KillSwitch = KillSwitchConnected
	}
}

func profileRoutingRules(p *Profile) []config.RoutingRule {
	if len(p.RoutingRules) > 0 {
		return p.RoutingRules
	}
	return p.Routing.Rules
}

func validateServerSelector(sel ServerSelector) error {
	switch sel.Mode {
	case "", "auto":
		return nil
	case "specific":
		if strings.TrimSpace(sel.ServerID) == "" {
			return errors.New("server_selector.server_id is required")
		}
	case "subscription_auto":
		return nil
	default:
		return errors.New("server_selector.mode: specific | auto | subscription_auto")
	}
	return nil
}

func validKillSwitchMode(mode KillSwitchMode) bool {
	switch mode {
	case KillSwitchOff, KillSwitchConnected, KillSwitchAlways:
		return true
	default:
		return false
	}
}

func profilePath(filename string) (string, error) {
	if filename == "" || filename != filepath.Base(filename) || !strings.HasSuffix(filename, ".json") || isReservedWindowsDeviceFilename(filename) {
		return "", errors.New("invalid profile filename")
	}
	return filepath.Join(profilesDir, filename), nil
}

func isReservedWindowsDeviceFilename(filename string) bool {
	base := strings.TrimSuffix(filename, filepath.Ext(filename))
	name := strings.ToUpper(strings.TrimSpace(base))
	switch name {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if len(name) == 4 && (strings.HasPrefix(name, "COM") || strings.HasPrefix(name, "LPT")) {
		return name[3] >= '1' && name[3] <= '9'
	}
	return false
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
