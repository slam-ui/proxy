package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"proxyclient/internal/config"
	"proxyclient/internal/fileutil"

	"github.com/gorilla/mux"
)

// noProxyTransport — HTTP транспорт без системного прокси.
// C-5: используется при загрузке subscription URL чтобы обойти возможные петли.
var noProxyTransport = &http.Transport{
	Proxy: nil,
	DialContext: (&net.Dialer{
		Timeout: 10 * time.Second,
	}).DialContext,
	TLSHandshakeTimeout: 10 * time.Second,
}

const serversFile = "servers.json"

// ServerEntry — сохранённый сервер в servers.json
type ServerEntry struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	URL             string `json:"url"`
	CountryCode     string `json:"country_code"` // RU, DE, NL, US, ...
	AddedAt         int64  `json:"added_at"`
	SubscriptionURL string `json:"subscription_url,omitempty"` // C-5: URL субскрипции для /refresh
}

// ServersHandlers управляет списком серверов и активным подключением
type ServersHandlers struct {
	server     *Server
	mu         sync.RWMutex
	secretKey  string                            // путь до active secret.key
	fetchURLFn func(rawURL string) (string, error) // C-5: инъекция для тестов (nil → fetchVLESSFromURL)
}

// SetupServerRoutes регистрирует маршруты менеджера серверов.
// Возвращает *ServersHandlers — позволяет тестам подменить fetchURLFn.
func SetupServerRoutes(s *Server, secretKeyPath string) *ServersHandlers {
	h := &ServersHandlers{server: s, secretKey: secretKeyPath}
	api := s.router.PathPrefix("/api").Subrouter()
	api.HandleFunc("/servers", h.handleList).Methods("GET", "OPTIONS")
	api.HandleFunc("/servers", h.handleAdd).Methods("POST", "OPTIONS")
	api.HandleFunc("/servers/{id}", h.handleDelete).Methods("DELETE", "OPTIONS")
	api.HandleFunc("/servers/{id}/connect", h.handleConnect).Methods("POST", "OPTIONS")
	api.HandleFunc("/servers/{id}/ping", h.handlePing).Methods("GET", "OPTIONS")
	api.HandleFunc("/servers/{id}/real-ping", h.handleRealPing).Methods("GET", "OPTIONS") // B-3
	api.HandleFunc("/servers/ping-all", h.handlePingAll).Methods("GET", "OPTIONS")
	api.HandleFunc("/servers/auto-connect", h.handleAutoConnect).Methods("POST", "OPTIONS")         // B-4
	api.HandleFunc("/servers/import-clipboard", h.handleImportClipboard).Methods("POST", "OPTIONS") // B-6
	api.HandleFunc("/servers/fetch-url", h.handleFetchURL).Methods("POST", "OPTIONS")               // C-5
	api.HandleFunc("/servers/{id}/refresh", h.handleRefresh).Methods("POST", "OPTIONS")             // C-5
	return h
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
	// BUG FIX (mtime-кэш): явно инвалидируем кэш vlessCache после записи нового secret.key.
	// На Windows WriteAtomic использует MoveFileExW — mtime нового файла берётся от temp-файла.
	// При быстрых последовательных записях (разрешение часов ~15мс) T_old == T_new →
	// mtime.Equal() = true → parseVLESSKey вернёт параметры старого сервера → конфиг
	// сгенерируется с тем же сервером A. Явный сброс params=nil гарантирует перечтение файла.
	config.InvalidateVLESSCache()
	if h.server.config.SecretKeyUpdatedFn != nil {
		h.server.config.SecretKeyUpdatedFn()
	}

	// BUG FIX: автоматически регенерируем конфиг sing-box и перезапускаем процесс.
	// Ранее handleConnect только писал secret.key и возвращал restart_required=true,
	// не выполняя никакого рестарта — трафик продолжал идти через старый сервер.
	// TriggerApply воспроизводит логику handleApply: GenerateSingBoxConfig → doApply.
	applyMsg := fmt.Sprintf("переключение на %q запущено — перезапуск sing-box", target.Name)
	restartRequired := false
	if h.server.tunHandlers != nil {
		if applyErr := h.server.tunHandlers.TriggerApply(); applyErr != nil {
			// TriggerApply уже запущен (конкурентный вызов) — пользователь должен подождать.
			h.server.logger.Warn("handleConnect: TriggerApply: %v", applyErr)
			applyMsg = fmt.Sprintf("secret.key обновлён для %q, но применение уже выполняется — подождите", target.Name)
			restartRequired = true
		}
	} else {
		// tunHandlers не инициализирован (например в тестах без TUN) — fallback на ручной рестарт.
		applyMsg = fmt.Sprintf("активен %q — перезапустите прокси чтобы применить", target.Name)
		restartRequired = true
	}

	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"success":          true,
		"message":          applyMsg,
		"restart_required": restartRequired,
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

	// B-5: используем 3 пробы для более точного измерения
	ms, minMs, maxMs, ok := pingServerWithProbes(target.URL, 3)
	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"id":         target.ID,
		"latency_ms": ms,
		"ok":         ok,
		"method":     "tcp", // B-3: TCP RTT измеритель
		"probes":     3,     // B-5: количество пробов
		"median_ms":  ms,    // B-5: медиана (основное значение)
		"min_ms":     minMs, // B-5: минимум
		"max_ms":     maxMs, // B-5: максимум
	})
}

// B-3: handleRealPing GET /api/servers/{id}/real-ping — HTTP тест через прокси
func (h *ServersHandlers) handleRealPing(w http.ResponseWriter, r *http.Request) {
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

	// Тестируем через локальный прокси (127.0.0.1:10807)
	ms, ok := pingThroughProxy(config.ProxyAddr, 10*time.Second)
	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"id":         target.ID,
		"latency_ms": ms,
		"ok":         ok,
		"method":     "http", // B-3: HTTP через прокси
	})
}

// B-4: handleAutoConnect POST /api/servers/auto-connect — автоматически подключиться к серверу с минимальной задержкой
func (h *ServersHandlers) handleAutoConnect(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	list, _ := loadServers()
	h.mu.RUnlock()

	if len(list) == 0 {
		h.server.respondError(w, http.StatusBadRequest, "нет доступных серверов")
		return
	}

	// B-4: пингуем все серверы параллельно
	type pingResult struct {
		id      string
		latency int64
		ok      bool
	}
	results := make([]pingResult, len(list))
	var wg sync.WaitGroup
	for i, srv := range list {
		wg.Add(1)
		go func(i int, srv ServerEntry) {
			defer wg.Done()
			// B-5: для auto-connect используем 1 пробу (приоритет скорость)
			ms, _, _, ok := pingServerWithProbes(srv.URL, 1)
			results[i] = pingResult{id: srv.ID, latency: ms, ok: ok}
		}(i, srv)
	}
	wg.Wait()

	// B-4: находим сервер с минимальной задержкой среди успешных
	var bestResult *pingResult
	for i := range results {
		if results[i].ok {
			if bestResult == nil || results[i].latency < bestResult.latency {
				bestResult = &results[i]
			}
		}
	}

	if bestResult == nil {
		h.server.respondError(w, http.StatusServiceUnavailable, "не удалось пропинговать ни один сервер")
		return
	}

	// B-4: проверяем нужно ли менять сервер (threshold 50ms)
	const latencyThreshold = 50 // ms
	currentID := h.activeServerID()
	changed := currentID != bestResult.id
	var currentLatency int64
	if changed {
		// Если меняется, получаем задержку текущего сервера
		for _, r := range results {
			if r.id == currentID && r.ok {
				currentLatency = r.latency
				break
			}
		}
		// Если текущий работает, проверяем threshold
		if currentLatency > 0 && (currentLatency-bestResult.latency) < int64(latencyThreshold) {
			// Разница меньше threshold — не переключаемся
			h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
				"connected_id":    currentID,
				"latency_ms":      currentLatency,
				"changed":         false,
				"reason":          "текущий сервер близок по задержке к оптимальному",
				"best_latency_ms": bestResult.latency,
			})
			return
		}
	}

	// B-4: подключаемся к лучшему серверу
	// Записываем URL в secret.key + запускаем перезапуск
	var bestServer *ServerEntry
	for i := range list {
		if list[i].ID == bestResult.id {
			bestServer = &list[i]
			break
		}
	}
	if bestServer == nil {
		h.server.respondError(w, http.StatusInternalServerError, "внутренняя ошибка: сервер не найден")
		return
	}

	if err := fileutil.WriteAtomic(h.secretKey, []byte(bestServer.URL), 0644); err != nil {
		h.server.respondError(w, http.StatusInternalServerError, "не удалось обновить secret.key: "+err.Error())
		return
	}
	config.InvalidateVLESSCache()

	// Запускаем перезапуск sing-box
	if h.server.tunHandlers != nil {
		if applyErr := h.server.tunHandlers.TriggerApply(); applyErr != nil {
			h.server.logger.Warn("handleAutoConnect: TriggerApply: %v", applyErr)
		}
	}

	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"connected_id": bestResult.id,
		"latency_ms":   bestResult.latency,
		"changed":      changed,
		"previous_id":  currentID,
	})
}

// B-6: handleImportClipboard POST /api/servers/import-clipboard — импортировать VLESS URL из буфера обмена.
// Тело запроса: {"url":"vless://..."}
// Валидирует URL, генерирует имя сервера из хоста, и автоактивирует если это первый сервер.
// Response codes: 200 (успех), 400 (невалидный URL), 409 (сервер уже существует)
func (h *ServersHandlers) handleImportClipboard(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.server.respondError(w, http.StatusBadRequest, "неверный JSON: "+err.Error())
		return
	}

	req.URL = strings.TrimSpace(strings.TrimPrefix(req.URL, "\xef\xbb\xbf"))
	if req.URL == "" {
		h.server.respondError(w, http.StatusBadRequest, "URL не должен быть пуст")
		return
	}

	// B-6: валидируем VLESS URL через parseVLESSContent
	params, err := config.ParseVLESSContent(req.URL)
	if err != nil {
		h.server.respondError(w, http.StatusBadRequest, "невалидный VLESS URL: "+err.Error())
		return
	}

	// B-6: генерируем имя из хоста (например, "Сервер my.server.com")
	hostname := params.Address
	if hostname == "" {
		hostname = "unknown"
	}
	serverName := fmt.Sprintf("Сервер %s", hostname)

	h.mu.Lock()
	defer h.mu.Unlock()

	list, err := loadServers()
	if err != nil {
		h.server.respondError(w, http.StatusInternalServerError, "ошибка чтения списка серверов")
		return
	}

	// B-6: проверяем дублекаты
	for _, s := range list {
		if s.URL == req.URL {
			h.server.respondJSON(w, http.StatusConflict, map[string]interface{}{
				"error":     "сервер с таким URL уже существует",
				"server_id": s.ID,
			})
			return
		}
	}

	// B-6: добавляем новый сервер
	entry := ServerEntry{
		ID:          fmt.Sprintf("%d", time.Now().UnixNano()),
		Name:        serverName,
		URL:         req.URL,
		CountryCode: "??",
		AddedAt:     time.Now().Unix(),
	}
	list = append(list, entry)
	if err := saveServers(list); err != nil {
		h.server.respondError(w, http.StatusInternalServerError, "ошибка записи: "+err.Error())
		return
	}

	// B-6: автоактивируем если это первый сервер
	if len(list) == 1 {
		_ = fileutil.WriteAtomic(h.secretKey, []byte(entry.URL), 0644)
		config.InvalidateVLESSCache()
	}

	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"server":  entry,
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
		MinMs     int64  `json:"min_ms"` // B-5
		MaxMs     int64  `json:"max_ms"` // B-5
		OK        bool   `json:"ok"`
		Probes    int    `json:"probes"` // B-5
	}
	results := make([]result, len(list))
	var wg sync.WaitGroup
	for i, srv := range list {
		wg.Add(1)
		go func(i int, srv ServerEntry) {
			defer wg.Done()
			// B-5: для ping-all используем 1 пробу (приоритет скорость)
			ms, minMs, maxMs, ok := pingServerWithProbes(srv.URL, 1)
			results[i] = result{ID: srv.ID, LatencyMs: ms, MinMs: minMs, MaxMs: maxMs, OK: ok, Probes: 1}
		}(i, srv)
	}
	wg.Wait()
	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{"results": results})
}

// ── Latency check ────────────────────────────────────────────────────────────

// B-5: medianInt64 вычисляет медиану массива значений int64.
// Требует непустой срез.
func medianInt64(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	if len(values) == 1 {
		return values[0]
	}

	// Копируем и сортируем
	sorted := make([]int64, len(values))
	copy(sorted, values)
	// Простая сортировка для малых массивов (обычно 1-3 элемента)
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j] < sorted[i] {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	// Для чётного количества элементов возвращаем среднее арифметическое двух средних
	return (sorted[mid-1] + sorted[mid]) / 2
}

// B-5: pingServerWithProbes измеряет TCP RTT с несколькими пробами и возвращает медиану, минимум и максимум.
// probes — количество пробов (обычно 1 или 3).
// Последовательные пробы разделены 200ms паузой untuk избежания скачков сетевых состояний.
// Возвращает: (медиана_мс, минимум_мс, максимум_мс, успех).
func pingServerWithProbes(vlessURL string, probes int) (int64, int64, int64, bool) {
	if probes <= 0 {
		probes = 1
	}

	addr := extractAddr(vlessURL)
	if addr == "" {
		return 0, 0, 0, false
	}

	const timeout = 5 * time.Second
	const pauseBetweenProbes = 200 * time.Millisecond

	var measurements []int64
	successCount := 0

	for i := 0; i < probes; i++ {
		if i > 0 {
			// Пауза между пробами для стабилизации сетевого состояния
			time.Sleep(pauseBetweenProbes)
		}

		// Измеряем только время установки TCP-соединения (SYN → SYN-ACK).
		start := time.Now()
		conn, err := net.DialTimeout("tcp", addr, timeout)
		if err != nil {
			continue
		}
		ms := time.Since(start).Milliseconds()
		conn.Close()

		measurements = append(measurements, ms)
		successCount++
	}

	// Если больше 50% пробов провалились, считаем это ошибкой
	if successCount <= probes/2 {
		return 0, 0, 0, false
	}

	// Вычисляем медиану
	med := medianInt64(measurements)

	// Находим минимум и максимум
	minMs := measurements[0]
	maxMs := measurements[0]
	for _, m := range measurements {
		if m < minMs {
			minMs = m
		}
		if m > maxMs {
			maxMs = m
		}
	}

	return med, minMs, maxMs, true
}

// pingServer измеряет чистый TCP RTT до сервера из VLESS URL (одна проба).
// Мы НЕ делаем TLS handshake — он добавляет 200-400ms к Reality/VLESS серверам
// и не отражает реальную сетевую задержку.
// Deprecated: используй pingServerWithProbes вместо этого.
func pingServer(vlessURL string) (int64, bool) {
	ms, _, _, ok := pingServerWithProbes(vlessURL, 1)
	return ms, ok
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

// ── C-5: Subscription URL ────────────────────────────────────────────────────

// validateSubscriptionURL проверяет что URL подходит для subscription загрузки:
// только http/https схемы, не localhost.
func validateSubscriptionURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("неверный URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("разрешены только http:// и https:// URL")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || host == "localhost" {
		return fmt.Errorf("localhost не разрешён")
	}
	// Блокируем loopback IP-адреса (127.x.x.x, ::1 и др.)
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return fmt.Errorf("loopback адреса не разрешены")
	}
	return nil
}

// fetchVLESSFromURL загружает URL и возвращает первый найденный VLESS URI.
// Порядок разбора: 1) построчный поиск vless://, 2) Base64-decode (V2ray subscription).
func fetchVLESSFromURL(rawURL string) (string, error) {
	client := &http.Client{
		Timeout:   15 * time.Second,
		Transport: noProxyTransport,
	}
	resp, err := client.Get(rawURL)
	if err != nil {
		return "", fmt.Errorf("ошибка загрузки: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d от сервера", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10)) // 64 KB
	if err != nil {
		return "", fmt.Errorf("ошибка чтения ответа: %w", err)
	}
	content := string(body)

	// Шаг 1: поиск vless:// построчно
	if v := findFirstVLESS(content); v != "" {
		return v, nil
	}
	// Шаг 2: Base64 decode (формат V2ray subscription)
	if decoded, err := base64DecodeSubscription(content); err == nil {
		if v := findFirstVLESS(decoded); v != "" {
			return v, nil
		}
	}
	return "", fmt.Errorf("не найдено VLESS URL в ответе")
}

// findFirstVLESS возвращает первую строку начинающуюся с vless://, или "".
func findFirstVLESS(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "vless://") {
			return line
		}
	}
	return ""
}

// base64DecodeSubscription пробует несколько вариантов Base64 (std, URL, raw).
func base64DecodeSubscription(content string) (string, error) {
	content = strings.TrimSpace(content)
	// Пробуем по порядку: StdEncoding → URLEncoding → RawStdEncoding
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
	} {
		if b, err := enc.DecodeString(content); err == nil {
			return string(b), nil
		}
	}
	return "", fmt.Errorf("не удалось декодировать Base64")
}

// C-5: handleFetchURL POST /api/servers/fetch-url — добавить сервер из URL субскрипции.
// Body: {"url":"https://...", "name":"optional name"}
// Скачивает URL, ищет VLESS URI (построчно или Base64), добавляет сервер.
func (h *ServersHandlers) handleFetchURL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL  string `json:"url"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.server.respondError(w, http.StatusBadRequest, "неверный JSON: "+err.Error())
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	if err := validateSubscriptionURL(req.URL); err != nil {
		h.server.respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	fetchFn := fetchVLESSFromURL
	if h.fetchURLFn != nil {
		fetchFn = h.fetchURLFn
	}
	vlessURI, err := fetchFn(req.URL)
	if err != nil {
		h.server.respondError(w, http.StatusUnprocessableEntity, "не удалось получить VLESS: "+err.Error())
		return
	}

	// Генерируем имя из хоста VLESS-сервера если не задано явно
	name := strings.TrimSpace(req.Name)
	if name == "" {
		if params, parseErr := config.ParseVLESSContent(vlessURI); parseErr == nil && params.Address != "" {
			name = "Сервер " + params.Address
		} else {
			name = "Сервер"
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	list, err := loadServers()
	if err != nil {
		h.server.respondError(w, http.StatusInternalServerError, "ошибка чтения списка серверов")
		return
	}
	// Проверка дублей по VLESS URI
	for _, s := range list {
		if s.URL == vlessURI {
			h.server.respondJSON(w, http.StatusConflict, map[string]interface{}{
				"exists":    true,
				"server_id": s.ID,
			})
			return
		}
	}

	entry := ServerEntry{
		ID:              fmt.Sprintf("%d", time.Now().UnixNano()),
		Name:            name,
		URL:             vlessURI,
		CountryCode:     "??",
		AddedAt:         time.Now().Unix(),
		SubscriptionURL: req.URL, // C-5: сохраняем для последующего /refresh
	}
	list = append(list, entry)
	if err := saveServers(list); err != nil {
		h.server.respondError(w, http.StatusInternalServerError, "ошибка записи: "+err.Error())
		return
	}
	// Автоактивация первого сервера
	if len(list) == 1 {
		_ = fileutil.WriteAtomic(h.secretKey, []byte(entry.URL), 0644)
		config.InvalidateVLESSCache()
	}
	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"server":  entry,
	})
}

// C-5: handleRefresh POST /api/servers/{id}/refresh — обновить сервер по сохранённому subscription URL.
// Повторно скачивает subscription_url и обновляет URL сервера если он изменился.
func (h *ServersHandlers) handleRefresh(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	h.mu.Lock()
	defer h.mu.Unlock()

	list, err := loadServers()
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
	if target.SubscriptionURL == "" {
		h.server.respondError(w, http.StatusBadRequest, "у сервера нет subscription URL — используйте /fetch-url для добавления")
		return
	}

	newVLESS, err := fetchVLESSFromURL(target.SubscriptionURL)
	if err != nil {
		h.server.respondError(w, http.StatusUnprocessableEntity, "ошибка обновления: "+err.Error())
		return
	}

	changed := target.URL != newVLESS
	if changed {
		target.URL = newVLESS
		if err := saveServers(list); err != nil {
			h.server.respondError(w, http.StatusInternalServerError, "ошибка записи: "+err.Error())
			return
		}
		config.InvalidateVLESSCache()
	}
	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"changed": changed,
		"server":  *target,
	})
}

// B-3: pingThroughProxy измеряет реальное соединение через локальный прокси.
// Отправляет GET запрос к https://cp.cloudflare.com/ (204 No Content, минимальный трафик).
// Возвращает время в миллисекундах до первого ответного байта, или (0, false) при ошибке.
func pingThroughProxy(proxyAddr string, timeout time.Duration) (int64, bool) {
	// Парсим адрес прокси формата "127.0.0.1:10807"
	proxyURL, err := url.Parse("http://" + proxyAddr)
	if err != nil {
		return 0, false
	}

	// Создаём HTTP клиент с установленным прокси
	tr := &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
		DialContext: (&net.Dialer{
			Timeout: timeout,
		}).DialContext,
		TLSHandshakeTimeout: timeout,
	}

	client := &http.Client{
		Transport: tr,
		Timeout:   timeout,
	}
	defer client.CloseIdleConnections()

	// Используем эндпоинт который возвращает 204 с минимальным трафиком
	start := time.Now()
	resp, err := client.Get("https://cp.cloudflare.com/")
	ms := time.Since(start).Milliseconds()

	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()

	// Проверяем что ответ был успешным (204 No Content или 2xx)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, false
	}

	return ms, true
}
