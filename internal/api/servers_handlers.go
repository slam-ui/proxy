package api

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"proxyclient/internal/fileutil"

	"github.com/gorilla/mux"
)

const serversFile = "servers.json"

// ServerEntry — сохранённый сервер в servers.json
type ServerEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	CountryCode string `json:"country_code"` // RU, DE, NL, US, ...
	AddedAt     int64  `json:"added_at"`
}

// ServersHandlers управляет списком серверов и активным подключением
type ServersHandlers struct {
	server    *Server
	mu        sync.RWMutex
	secretKey string // путь до active secret.key
}

// SetupServerRoutes регистрирует маршруты менеджера серверов
func SetupServerRoutes(s *Server, secretKeyPath string) {
	h := &ServersHandlers{server: s, secretKey: secretKeyPath}
	api := s.router.PathPrefix("/api").Subrouter()
	api.HandleFunc("/servers", h.handleList).Methods("GET", "OPTIONS")
	api.HandleFunc("/servers", h.handleAdd).Methods("POST", "OPTIONS")
	api.HandleFunc("/servers/{id}", h.handleDelete).Methods("DELETE", "OPTIONS")
	api.HandleFunc("/servers/{id}/connect", h.handleConnect).Methods("POST", "OPTIONS")
	api.HandleFunc("/servers/{id}/ping", h.handlePing).Methods("GET", "OPTIONS")
	api.HandleFunc("/servers/ping-all", h.handlePingAll).Methods("GET", "OPTIONS")
}

// ── Persistence ─────────────────────────────────────────────────────────────

func loadServers() ([]ServerEntry, error) {
	data, err := os.ReadFile(serversFile)
	if err != nil {
		if os.IsNotExist(err) {
			return []ServerEntry{}, nil
		}
		return nil, err
	}
	var list []ServerEntry
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	return list, nil
}

func saveServers(list []ServerEntry) error {
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	// BUG FIX: os.WriteFile не атомарен — при крэше в середине записи servers.json
	// будет повреждён. fileutil.WriteAtomic использует MoveFileExW (NTFS-транзакция).
	return fileutil.WriteAtomic(serversFile, data, 0644)
}

// activeServerID возвращает ID активного сервера (совпадение URL с secret.key)
func (h *ServersHandlers) activeServerID() string {
	raw, err := os.ReadFile(h.secretKey)
	if err != nil {
		return ""
	}
	active := strings.TrimSpace(string(raw))
	active = strings.TrimPrefix(active, "\xef\xbb\xbf") // strip BOM
	list, _ := loadServers()
	for _, s := range list {
		if strings.TrimSpace(s.URL) == active {
			return s.ID
		}
	}
	return ""
}

// ── Handlers ─────────────────────────────────────────────────────────────────

// GET /api/servers
func (h *ServersHandlers) handleList(w http.ResponseWriter, _ *http.Request) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	list, err := loadServers()
	if err != nil {
		h.server.respondError(w, http.StatusInternalServerError, "не удалось прочитать servers.json: "+err.Error())
		return
	}
	// Если secret.key существует но не в списке — добавляем виртуальный «текущий»
	activeID := h.activeServerID()
	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"servers":   list,
		"active_id": activeID,
	})
}

// POST /api/servers  body: {name, url, country_code}
func (h *ServersHandlers) handleAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		URL         string `json:"url"`
		CountryCode string `json:"country_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.server.respondError(w, http.StatusBadRequest, "неверный JSON: "+err.Error())
		return
	}
	req.URL = strings.TrimSpace(strings.TrimPrefix(req.URL, "\xef\xbb\xbf"))
	if !strings.HasPrefix(req.URL, "vless://") {
		h.server.respondError(w, http.StatusBadRequest, "URL должен начинаться с vless://")
		return
	}
	if req.Name == "" {
		req.Name = "Сервер"
	}
	if req.CountryCode == "" {
		req.CountryCode = "??"
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	list, err := loadServers()
	if err != nil {
		h.server.respondError(w, http.StatusInternalServerError, "ошибка чтения списка серверов")
		return
	}
	// Проверка дублей
	for _, s := range list {
		if s.URL == req.URL {
			h.server.respondError(w, http.StatusConflict, "сервер с таким URL уже существует")
			return
		}
	}

	entry := ServerEntry{
		ID:          fmt.Sprintf("%d", time.Now().UnixNano()),
		Name:        req.Name,
		URL:         req.URL,
		CountryCode: strings.ToUpper(req.CountryCode),
		AddedAt:     time.Now().Unix(),
	}
	list = append(list, entry)
	if err := saveServers(list); err != nil {
		h.server.respondError(w, http.StatusInternalServerError, "ошибка записи: "+err.Error())
		return
	}
	// Если это первый сервер и secret.key не существует — активируем автоматически
	// BUG FIX: используем fileutil.WriteAtomic — secret.key критичен, его повреждение
	// делает приложение неработоспособным.
	if len(list) == 1 {
		_ = fileutil.WriteAtomic(h.secretKey, []byte(entry.URL), 0644)
	}
	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"server":  entry,
	})
}

// DELETE /api/servers/{id}
func (h *ServersHandlers) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	h.mu.Lock()
	defer h.mu.Unlock()

	list, err := loadServers()
	if err != nil {
		h.server.respondError(w, http.StatusInternalServerError, "ошибка чтения")
		return
	}
	newList := list[:0]
	found := false
	for _, s := range list {
		if s.ID == id {
			found = true
		} else {
			newList = append(newList, s)
		}
	}
	if !found {
		h.server.respondError(w, http.StatusNotFound, "сервер не найден")
		return
	}
	if err := saveServers(newList); err != nil {
		h.server.respondError(w, http.StatusInternalServerError, "ошибка записи")
		return
	}
	h.server.respondJSON(w, http.StatusOK, MessageResponse{Success: true, Message: "сервер удалён"})
}

// POST /api/servers/{id}/connect — сделать этот сервер активным и перезапустить sing-box
func (h *ServersHandlers) handleConnect(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	h.mu.RLock()
	list, err := loadServers()
	h.mu.RUnlock()

	if err != nil {
		h.server.respondError(w, http.StatusInternalServerError, "ошибка чтения")
		return
	}
	var target *ServerEntry
	for i := range list {
		if list[i].ID == id {
			target = &list[i]
			break
		}
	}
	if target == nil {
		h.server.respondError(w, http.StatusNotFound, "сервер не найден")
		return
	}

	// Записываем URL в secret.key атомарно.
	// BUG FIX: os.WriteFile не атомарен — при крэше secret.key будет повреждён
	// и приложение не запустится. fileutil.WriteAtomic использует MoveFileExW.
	if err := fileutil.WriteAtomic(h.secretKey, []byte(target.URL), 0644); err != nil {
		h.server.respondError(w, http.StatusInternalServerError, "не удалось обновить secret.key: "+err.Error())
		return
	}

	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("активен %q — перезапустите прокси чтобы применить", target.Name),
		"restart_required": true,
	})
}

// GET /api/servers/{id}/ping — TCP + TLS latency к серверу
func (h *ServersHandlers) handlePing(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	h.mu.RLock()
	list, _ := loadServers()
	h.mu.RUnlock()

	var target *ServerEntry
	for i := range list {
		if list[i].ID == id {
			target = &list[i]
			break
		}
	}
	if target == nil {
		h.server.respondError(w, http.StatusNotFound, "сервер не найден")
		return
	}

	ms, ok := pingServer(target.URL)
	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"id":         target.ID,
		"latency_ms": ms,
		"ok":         ok,
	})
}

// GET /api/servers/ping-all — пингуем все серверы параллельно
func (h *ServersHandlers) handlePingAll(w http.ResponseWriter, _ *http.Request) {
	h.mu.RLock()
	list, _ := loadServers()
	h.mu.RUnlock()

	type result struct {
		ID        string `json:"id"`
		LatencyMs int64  `json:"latency_ms"`
		OK        bool   `json:"ok"`
	}
	results := make([]result, len(list))
	var wg sync.WaitGroup
	for i, srv := range list {
		wg.Add(1)
		go func(i int, srv ServerEntry) {
			defer wg.Done()
			ms, ok := pingServer(srv.URL)
			results[i] = result{ID: srv.ID, LatencyMs: ms, OK: ok}
		}(i, srv)
	}
	wg.Wait()
	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{"results": results})
}

// ── Latency check ────────────────────────────────────────────────────────────

// pingServer измеряет чистый TCP RTT до сервера из VLESS URL.
// Мы НЕ делаем TLS handshake — он добавляет 200-400ms к Reality/VLESS серверам
// и не отражает реальную сетевую задержку.
func pingServer(vlessURL string) (int64, bool) {
	addr := extractAddr(vlessURL)
	if addr == "" {
		return 0, false
	}

	const timeout = 5 * time.Second

	// Измеряем только время установки TCP-соединения (SYN → SYN-ACK).
	// Это настоящий network RTT без overhead протоколов прикладного уровня.
	start := time.Now()
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return 0, false
	}
	ms := time.Since(start).Milliseconds()
	conn.Close()
	return ms, true
}

// extractAddr вытаскивает host:port из VLESS URL
func extractAddr(vlessURL string) string {
	// vless://uuid@host:port?params#name
	u := vlessURL
	// убираем схему
	u = strings.TrimPrefix(u, "vless://")
	// убираем фрагмент
	if i := strings.Index(u, "#"); i >= 0 {
		u = u[:i]
	}
	// убираем query
	if i := strings.Index(u, "?"); i >= 0 {
		u = u[:i]
	}
	// убираем path
	if i := strings.Index(u, "/"); i >= 0 {
		u = u[:i]
	}
	// uuid@host:port → host:port
	if i := strings.Index(u, "@"); i >= 0 {
		u = u[i+1:]
	}
	if u == "" {
		return ""
	}
	return u
}
