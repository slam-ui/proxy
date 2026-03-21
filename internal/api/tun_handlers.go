package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"proxyclient/internal/config"
	"proxyclient/internal/netutil"
	"proxyclient/internal/proxy"
	"proxyclient/internal/xray"

	"github.com/gorilla/mux"
)

var routingConfigPath = config.DataDir + "/routing.json"

// isValidAction возвращает true если action — одно из допустимых значений.
// BUG FIX #18: валидация action дублировалась в 4 местах (handleAddRule,
// handleBulkReplaceRules, handleSetDefault, handleImport). Вынесена в функцию.
func isValidAction(a config.RuleAction) bool {
	return a == config.ActionProxy || a == config.ActionDirect || a == config.ActionBlock
}

// applyState хранит состояние последнего применения правил
type applyState struct {
	mu            sync.Mutex
	running       bool
	lastErr       string
	lastPID       int
	startedAt     time.Time // когда начался apply
	estimatedDone time.Time // оценочное время завершения
}

// TunHandlers обработчики маршрутизации
type TunHandlers struct {
	server       *Server
	xrayConfig   xray.Config
	proxyManager proxy.Manager
	mu           sync.RWMutex
	routing      *config.RoutingConfig
	apply        applyState
}

// SetupTunRoutes регистрирует маршруты
func (s *Server) SetupTunRoutes(xrayCfg xray.Config) *TunHandlers {
	routing, err := config.LoadRoutingConfig(routingConfigPath)
	if err != nil {
		s.logger.Warn("Не удалось загрузить routing config: %v", err)
		routing = config.DefaultRoutingConfig()
	}

	h := &TunHandlers{
		server:       s,
		xrayConfig:   xrayCfg,
		proxyManager: s.config.ProxyManager,
		routing:      routing,
	}

	s.router.HandleFunc("/api/tun/rules", h.handleListRules).Methods("GET", "OPTIONS")
	s.router.HandleFunc("/api/tun/rules", h.handleAddRule).Methods("POST", "OPTIONS")
	// PUT /api/tun/rules — атомарная замена всего списка за один запрос.
	// Используется фронтендом вместо N последовательных DELETE+POST, что устраняет
	// зависание WebView2 при большом количестве правил (JS-тред блокировался на 30+ секунд).
	s.router.HandleFunc("/api/tun/rules", h.handleBulkReplaceRules).Methods("PUT", "OPTIONS")
	s.router.HandleFunc("/api/tun/rules/{value}", h.handleDeleteRule).Methods("DELETE", "OPTIONS")
	s.router.HandleFunc("/api/tun/default", h.handleSetDefault).Methods("POST", "OPTIONS")
	s.router.HandleFunc("/api/tun/apply", h.handleApply).Methods("POST", "OPTIONS")
	s.router.HandleFunc("/api/tun/apply/status", h.handleApplyStatus).Methods("GET", "OPTIONS")
	s.router.HandleFunc("/api/tun/export", h.handleExport).Methods("GET", "OPTIONS")
	s.router.HandleFunc("/api/tun/import", h.handleImport).Methods("POST", "OPTIONS")

	return h
}

// RulesResponse ответ GET /api/tun/rules
type RulesResponse struct {
	DefaultAction config.RuleAction    `json:"default_action"`
	Rules         []config.RoutingRule `json:"rules"`
}

// handleListRules GET /api/tun/rules
func (h *TunHandlers) handleListRules(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	resp := RulesResponse{
		DefaultAction: h.routing.DefaultAction,
		Rules:         h.routing.Rules,
	}
	if resp.Rules == nil {
		resp.Rules = []config.RoutingRule{}
	}
	h.server.respondJSON(w, http.StatusOK, resp)
}

// AddRuleRequest тело POST /api/tun/rules
type AddRuleRequest struct {
	Value  string            `json:"value"`
	Action config.RuleAction `json:"action"`
	Note   string            `json:"note"`
}

// handleAddRule POST /api/tun/rules
func (h *TunHandlers) handleAddRule(w http.ResponseWriter, r *http.Request) {
	var req AddRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.server.respondError(w, http.StatusBadRequest, "некорректное тело запроса")
		return
	}

	val := config.NormalizeRuleValue(req.Value)

	// BUG FIX: пустой val после стриппинга URL (например "https://") приводил к добавлению
	// правила с пустым значением, что генерировало некорректный sing-box конфиг.
	if strings.TrimSpace(val) == "" {
		h.server.respondError(w, http.StatusBadRequest, "пустое значение правила")
		return
	}

	ruleType := config.DetectRuleType(strings.ToLower(val))

	// FIX: process rules сохраняем в оригинальном регистре.
	// sing-box матчит process_name с учётом регистра на Windows — "telegram.exe" не совпадёт
	// с реальным процессом "Telegram.exe". Домены и IP приводим к lowercase, .exe — нет.
	if ruleType != config.RuleTypeProcess {
		val = strings.ToLower(val)
	}

	if !isValidAction(req.Action) {
		h.server.respondError(w, http.StatusBadRequest, "action: proxy | direct | block")
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	for _, rule := range h.routing.Rules {
		if rule.Value == val {
			h.server.respondError(w, http.StatusConflict, "правило уже существует")
			return
		}
	}

	newRule := config.RoutingRule{
		Value:  val,
		Type:   ruleType,
		Action: req.Action,
		Note:   req.Note,
	}
	h.routing.Rules = append(h.routing.Rules, newRule)

	// BUG FIX: при ошибке сохранения откатываем in-memory изменение и возвращаем ошибку клиенту.
	// Ранее возвращался 201 Created даже если файл не был записан — на следующем рестарте
	// правило исчезало без каких-либо предупреждений (тихая потеря данных).
	if err := config.SaveRoutingConfig(routingConfigPath, h.routing); err != nil {
		h.routing.Rules = h.routing.Rules[:len(h.routing.Rules)-1] // откат
		h.server.logger.Error("Не удалось сохранить routing config: %v", err)
		h.server.respondError(w, http.StatusInternalServerError, "не удалось сохранить правило")
		return
	}

	h.server.respondJSON(w, http.StatusCreated, map[string]interface{}{
		"message": "правило добавлено",
		"rule":    newRule,
	})
}

// BulkReplaceRequest тело PUT /api/tun/rules
type BulkReplaceRequest struct {
	DefaultAction config.RuleAction    `json:"default_action"`
	Rules         []config.RoutingRule `json:"rules"`
}

// handleBulkReplaceRules PUT /api/tun/rules — атомарно заменяет весь список правил.
// Вызывается фронтендом вместо N последовательных DELETE+POST: один HTTP-запрос
// вместо 50+, что предотвращает зависание WebView2.
func (h *TunHandlers) handleBulkReplaceRules(w http.ResponseWriter, r *http.Request) {
	var req BulkReplaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.server.respondError(w, http.StatusBadRequest, "некорректное тело запроса")
		return
	}

	// Валидируем каждое правило
	for i, rule := range req.Rules {
		val := strings.TrimSpace(rule.Value)
		if val == "" {
			h.server.respondError(w, http.StatusBadRequest,
				fmt.Sprintf("правило #%d: пустое значение", i+1))
			return
		}
		if !isValidAction(rule.Action) {
			h.server.respondError(w, http.StatusBadRequest,
				fmt.Sprintf("правило #%d: неверный action (proxy|direct|block)", i+1))
			return
		}
	}

	// Валидируем default_action (пустая строка → оставляем текущий)
	if req.DefaultAction != "" && !isValidAction(req.DefaultAction) {
		h.server.respondError(w, http.StatusBadRequest, "default_action: proxy | direct | block")
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	oldRules := h.routing.Rules
	oldDefault := h.routing.DefaultAction

	h.routing.Rules = req.Rules
	if req.DefaultAction != "" {
		h.routing.DefaultAction = req.DefaultAction
	}

	if err := config.SaveRoutingConfig(routingConfigPath, h.routing); err != nil {
		// Откат
		h.routing.Rules = oldRules
		h.routing.DefaultAction = oldDefault
		h.server.logger.Error("Bulk replace: не удалось сохранить routing config: %v", err)
		h.server.respondError(w, http.StatusInternalServerError, "не удалось сохранить правила")
		return
	}

	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"message": "правила обновлены",
		"count":   len(req.Rules),
	})
}

// handleDeleteRule DELETE /api/tun/rules/{value}
func (h *TunHandlers) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	value := strings.ToLower(vars["value"])

	h.mu.Lock()
	defer h.mu.Unlock()

	newRules := make([]config.RoutingRule, 0, len(h.routing.Rules))
	found := false
	for _, rule := range h.routing.Rules {
		if strings.ToLower(rule.Value) == value {
			found = true
			continue
		}
		newRules = append(newRules, rule)
	}

	if !found {
		h.server.respondError(w, http.StatusNotFound, "правило не найдено")
		return
	}

	oldRules := h.routing.Rules // сохраняем для отката
	h.routing.Rules = newRules

	// BUG FIX: при ошибке сохранения откатываем удаление и возвращаем ошибку клиенту.
	if err := config.SaveRoutingConfig(routingConfigPath, h.routing); err != nil {
		h.routing.Rules = oldRules // откат
		h.server.logger.Error("Не удалось сохранить routing config: %v", err)
		h.server.respondError(w, http.StatusInternalServerError, "не удалось сохранить изменения")
		return
	}

	h.server.respondJSON(w, http.StatusOK, MessageResponse{Message: "правило удалено", Success: true})
}

// handleSetDefault POST /api/tun/default
func (h *TunHandlers) handleSetDefault(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action config.RuleAction `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.server.respondError(w, http.StatusBadRequest, "некорректное тело запроса")
		return
	}
	if !isValidAction(req.Action) {
		h.server.respondError(w, http.StatusBadRequest, "action: proxy | direct | block")
		return
	}

	h.mu.Lock()
	oldAction := h.routing.DefaultAction
	h.routing.DefaultAction = req.Action

	// BUG FIX: при ошибке сохранения откатываем изменение и возвращаем ошибку клиенту.
	if err := config.SaveRoutingConfig(routingConfigPath, h.routing); err != nil {
		h.routing.DefaultAction = oldAction // откат
		h.mu.Unlock()
		h.server.logger.Error("Не удалось сохранить routing config: %v", err)
		h.server.respondError(w, http.StatusInternalServerError, "не удалось сохранить изменения")
		return
	}
	h.mu.Unlock()

	h.server.respondJSON(w, http.StatusOK, MessageResponse{Message: "дефолтное действие обновлено", Success: true})
}

// handleApply POST /api/tun/apply — запускает перезапуск асинхронно и сразу возвращает ответ
func (h *TunHandlers) handleApply(w http.ResponseWriter, r *http.Request) {
	h.apply.mu.Lock()
	if h.apply.running {
		h.apply.mu.Unlock()
		h.server.respondError(w, http.StatusConflict, "применение уже выполняется")
		return
	}
	h.apply.running = true
	h.apply.lastErr = ""
	h.apply.startedAt = time.Now()
	h.apply.estimatedDone = time.Now().Add(35 * time.Second) // BeforeRestart=30s + startup ~5s
	h.apply.mu.Unlock()

	h.mu.RLock()
	snapshot := &config.RoutingConfig{
		DefaultAction: h.routing.DefaultAction,
		Rules:         make([]config.RoutingRule, len(h.routing.Rules)),
	}
	copy(snapshot.Rules, h.routing.Rules)
	h.mu.RUnlock()

	// FIX: pre-validate конфиг до запуска горутины.
	// Аналог nekoray BuildConfig — строит конфиг в памяти и возвращает ошибку
	// ДО любых деструктивных действий. Sing-box продолжает работать если конфиг плохой.
	tmpConfigPath := h.xrayConfig.ConfigPath + ".pending"
	if err := config.GenerateSingBoxConfig(h.xrayConfig.SecretKeyPath, tmpConfigPath, snapshot); err != nil {
		// Проверяем: если geosite файла нет — предупреждаем но продолжаем со старым конфигом
		if _, statErr := os.Stat(h.xrayConfig.ConfigPath); statErr != nil {
			// Старого конфига нет — критично, отменяем
			h.apply.mu.Lock()
			h.apply.running = false
			h.apply.lastErr = err.Error()
			h.apply.mu.Unlock()
			h.server.respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		// Geosite не найден, но старый конфиг есть — логируем и используем его
		h.server.logger.Warn("pre-validate конфига не прошла (%v) — применим с существующим конфигом", err)
		_ = os.Remove(tmpConfigPath) // убираем неполный tmp
		tmpConfigPath = ""           // сигнал doApply: использовать существующий конфиг
	}

	h.server.respondJSON(w, http.StatusAccepted, map[string]interface{}{
		"message": "применение запущено",
		"rules":   len(snapshot.Rules),
	})

	go h.doApply(snapshot, tmpConfigPath)
}

// doApply выполняет перезапуск sing-box в фоновой горутине.
// tmpConfigPath — путь к предварительно сгенерированному конфигу (или "" если использовать существующий).
func (h *TunHandlers) doApply(snapshot *config.RoutingConfig, tmpConfigPath string) {
	setErr := func(err string) {
		h.apply.mu.Lock()
		h.apply.lastErr = err
		h.apply.mu.Unlock()
	}

	defer func() {
		h.apply.mu.Lock()
		h.apply.running = false
		h.apply.mu.Unlock()
	}()

	// Запоминаем состояние прокси и отключаем его на время рестарта,
	// чтобы трафик не уходил в недоступный sing-box.
	proxyWasEnabled := h.proxyManager.IsEnabled()
	proxyConfig := h.proxyManager.GetConfig()
	if proxyWasEnabled {
		if err := h.proxyManager.Disable(); err != nil {
			h.server.logger.Warn("Не удалось отключить прокси перед рестартом: %v", err)
		}
	}

	// Восстанавливаем прокси при выходе (только если sing-box успешно поднялся).
	// Флаг успеха выставляется явно в конце функции.
	restoreProxy := false
	defer func() {
		if restoreProxy && proxyWasEnabled {
			if err := h.proxyManager.Enable(proxyConfig); err != nil {
				h.server.logger.Error("Не удалось восстановить прокси после рестарта: %v", err)
			}
		}
	}()

	// Останавливаем текущий процесс.
	h.server.configMu.RLock()
	currentManager := h.server.config.XRayManager
	h.server.configMu.RUnlock()
	if currentManager != nil {
		if err := currentManager.Stop(); err != nil {
			h.server.logger.Warn("Ошибка при остановке: %v", err)
		}
	}

	// BeforeRestart выполняет wintun cleanup (RecordStop + RemoveStaleTunAdapter + PollUntilFree).
	// Инъектируется через xray.Config.BeforeRestart из main.go —
	// api пакет больше не зависит от wintun напрямую.
	if h.xrayConfig.BeforeRestart != nil {
		if err := h.xrayConfig.BeforeRestart(h.server.lifecycleCtx, h.server.logger); err != nil {
			h.server.logger.Warn("BeforeRestart вернул ошибку: %v", err)
		}
	}

	// Применяем предварительно сгенерированный конфиг (или оставляем существующий).
	// Конфиг уже был проверен в handleApply ДО остановки sing-box — здесь просто
	// переименовываем .pending → рабочий файл. Это гарантирует атомарную замену.
	if tmpConfigPath != "" {
		if err := os.Rename(tmpConfigPath, h.xrayConfig.ConfigPath); err != nil {
			h.server.logger.Error("Не удалось применить конфиг: %v", err)
			_ = os.Remove(tmpConfigPath)
			setErr(err.Error())
			return
		}
		h.server.logger.Info("Конфиг sing-box обновлён")
	} else {
		h.server.logger.Warn("Конфиг не обновлялся — применяем с существующим")
	}

	// Запускаем sing-box.
	// BUG FIX: OnCrash в xrayCfg замыкается на старую переменную xrayManager из run().
	// После doApply xrayManager не обновляется — OnCrash вызывал бы Start() на старом
	// (уже остановленном) менеджере. Подменяем OnCrash: читаем актуальный менеджер
	// из h.server.config.XRayManager который всегда актуален.
	patchedCfg := h.xrayConfig
	srv := h.server
	patchedCfg.OnCrash = func(crashErr error) {
		srv.configMu.RLock()
		cur := srv.config.XRayManager
		srv.configMu.RUnlock()
		if cur != nil && h.xrayConfig.OnCrash != nil {
			h.xrayConfig.OnCrash(crashErr)
		}
	}
	newManager, err := xray.NewManager(patchedCfg, h.server.lifecycleCtx)
	if err != nil {
		h.server.logger.Error("Не удалось запустить sing-box: %v", err)
		setErr(err.Error())
		return
	}

	// Ждём готовности sing-box.
	if err := netutil.WaitForPort(DefaultProxyAddress, 15*time.Second); err != nil {
		h.server.logger.Warn("sing-box не ответил за 15с после перезапуска: %v", err)
	}

	// Атомарно заменяем менеджер.
	h.server.configMu.Lock()
	h.server.config.XRayManager = newManager
	h.server.configMu.Unlock()

	h.apply.mu.Lock()
	h.apply.lastPID = newManager.GetPID()
	h.apply.mu.Unlock()

	h.server.logger.Info("Sing-box перезапущен (PID: %d), правил: %d", newManager.GetPID(), len(snapshot.Rules))

	// Всё прошло успешно — восстанавливаем прокси.
	restoreProxy = true
}

// handleApplyStatus GET /api/tun/apply/status — состояние последнего применения
func (h *TunHandlers) handleApplyStatus(w http.ResponseWriter, r *http.Request) {
	h.apply.mu.Lock()
	running := h.apply.running
	lastErr := h.apply.lastErr
	lastPID := h.apply.lastPID
	h.apply.mu.Unlock()

	elapsedMs := int64(0)
	estimatedRemainMs := int64(0)
	if !h.apply.startedAt.IsZero() {
		elapsedMs = time.Since(h.apply.startedAt).Milliseconds()
	}
	if !h.apply.estimatedDone.IsZero() && running {
		remaining := time.Until(h.apply.estimatedDone).Milliseconds()
		if remaining < 0 { remaining = 0 }
		estimatedRemainMs = remaining
	}
	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"running":              running,
		"last_err":             lastErr,
		"last_pid":             lastPID,
		"elapsed_ms":           elapsedMs,
		"estimated_remain_ms":  estimatedRemainMs,
		"estimated_total_ms":   35000,
	})
}

// handleExport GET /api/tun/export — скачивает routing.json как файл
func (h *TunHandlers) handleExport(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	data, err := json.MarshalIndent(&config.RoutingConfig{
		DefaultAction: h.routing.DefaultAction,
		Rules:         h.routing.Rules,
	}, "", "  ")
	if err != nil {
		h.server.respondError(w, http.StatusInternalServerError, "marshal error")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="routing.json"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// handleImport POST /api/tun/import — загружает routing.json, валидирует и сохраняет
// Принимает multipart/form-data с полем "file" или application/json напрямую.
func (h *TunHandlers) handleImport(w http.ResponseWriter, r *http.Request) {
	var raw []byte

	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "multipart/form-data") {
		// Загрузка через <input type="file">
		if err := r.ParseMultipartForm(1 << 20); err != nil { // 1 MB max
			h.server.respondError(w, http.StatusBadRequest, "failed to parse form")
			return
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			h.server.respondError(w, http.StatusBadRequest, "field 'file' missing")
			return
		}
		defer f.Close()
		var err2 error
		raw, err2 = io.ReadAll(f)
		if err2 != nil {
			h.server.respondError(w, http.StatusBadRequest, "failed to read file")
			return
		}
	} else {
		// Прямая отправка JSON
		var err error
		raw, err = io.ReadAll(r.Body)
		if err != nil {
			h.server.respondError(w, http.StatusBadRequest, "failed to read body")
			return
		}
	}

	var incoming config.RoutingConfig
	if err := json.Unmarshal(raw, &incoming); err != nil {
		h.server.respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	// Валидация
	if !isValidAction(incoming.DefaultAction) {
		h.server.respondError(w, http.StatusBadRequest, "invalid default_action")
		return
	}
	for i, rule := range incoming.Rules {
		if rule.Value == "" {
			h.server.respondError(w, http.StatusBadRequest,
				fmt.Sprintf("rule[%d]: value is empty", i))
			return
		}
		if !isValidAction(rule.Action) {
			h.server.respondError(w, http.StatusBadRequest,
				fmt.Sprintf("rule[%d]: invalid action %q", i, rule.Action))
			return
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if err := config.SaveRoutingConfig(routingConfigPath, &incoming); err != nil {
		h.server.respondError(w, http.StatusInternalServerError, "failed to save: "+err.Error())
		return
	}
	h.routing = &incoming

	h.server.respondJSON(w, http.StatusOK, MessageResponse{
		Success: true,
		Message: fmt.Sprintf("imported %d rules", len(incoming.Rules)),
	})
}
