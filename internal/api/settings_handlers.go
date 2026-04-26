package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"proxyclient/internal/autorun"
	"proxyclient/internal/config"
	"proxyclient/internal/killswitch"
)

// SettingsHandlers обработчики настроек приложения.
// Использует Server для доступа к respondJSON/respondError — согласованно с TunHandlers.
type SettingsHandlers struct {
	server *Server
}

// SetupSettingsRoutes регистрирует API для настроек приложения.
func SetupSettingsRoutes(s *Server) {
	h := &SettingsHandlers{server: s}
	s.router.HandleFunc("/api/settings", h.handleGetSettings).Methods("GET", "OPTIONS")
	s.router.HandleFunc("/api/settings/autorun", h.handleSetAutorun).Methods("POST", "OPTIONS")
	s.router.HandleFunc("/api/settings/startup-proxy", h.handleSetStartupProxy).Methods("POST", "OPTIONS")
	s.router.HandleFunc("/api/settings/killswitch", h.handleSetKillSwitch).Methods("POST", "OPTIONS")
	// B-2: Proxy Guard endpoints
	s.router.HandleFunc("/api/settings/proxy-guard", h.handleGetProxyGuard).Methods("GET", "OPTIONS")
	s.router.HandleFunc("/api/settings/proxy-guard", h.handleSetProxyGuard).Methods("POST", "OPTIONS")
	// B-7: DNS endpoints
	s.router.HandleFunc("/api/settings/dns", h.handleGetDNS).Methods("GET", "OPTIONS")
	s.router.HandleFunc("/api/settings/dns", h.handleSetDNS).Methods("POST", "OPTIONS")
	// B-10: Geosite auto-update endpoints
	s.router.HandleFunc("/api/settings/geosite-update", h.handleGetGeositeUpdate).Methods("GET", "OPTIONS")
	s.router.HandleFunc("/api/settings/geosite-update", h.handleSetGeositeUpdate).Methods("POST", "OPTIONS")
}

// SettingsResponse — состояние всех настроек приложения.
type SettingsResponse struct {
	Autorun            bool `json:"autorun"`               // включён ли автозапуск при входе в Windows
	KillSwitch         bool `json:"kill_switch"`           // активен ли Kill Switch прямо сейчас
	ProxyGuard         bool `json:"proxy_guard"`           // B-2: активна ли Proxy Guard для восстановления
	StartProxyOnLaunch bool `json:"start_proxy_on_launch"` // включать прокси сразу после запуска клиента
}

// handleGetSettings GET /api/settings
func (h *SettingsHandlers) handleGetSettings(w http.ResponseWriter, _ *http.Request) {
	appSettings, err := config.LoadAppSettings(config.AppSettingsFile)
	if err != nil {
		h.server.logger.Warn("handleGetSettings: %v", err)
		appSettings = config.DefaultAppSettings()
	}
	h.server.respondJSON(w, http.StatusOK, SettingsResponse{
		Autorun:            autorun.IsEnabled(),
		KillSwitch:         killswitch.IsEnabled(),
		ProxyGuard:         h.server.IsProxyGuardEnabled(),
		StartProxyOnLaunch: appSettings.StartProxyOnLaunch,
	})
}

// handleGetProxyGuard GET /api/settings/proxy-guard — получить статус Proxy Guard
// B-2: Возвращает {"enabled": bool}
func (h *SettingsHandlers) handleGetProxyGuard(w http.ResponseWriter, _ *http.Request) {
	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"enabled": h.server.IsProxyGuardEnabled(),
	})
}

// handleSetProxyGuard POST /api/settings/proxy-guard — включить/отключить Proxy Guard
// Body: {"enabled": true|false}
func (h *SettingsHandlers) handleSetProxyGuard(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.server.respondError(w, http.StatusBadRequest, "invalid body")
		return
	}

	h.server.SetProxyGuardEnabled(body.Enabled)
	if body.Enabled {
		if err := h.server.StartProxyGuard(); err != nil {
			h.server.logger.Warn("handleSetProxyGuard: StartGuard: %v", err)
		} else {
			h.server.logger.Info("Proxy Guard запущен через UI")
		}
	} else {
		h.server.StopProxyGuard()
		h.server.logger.Info("Proxy Guard остановлен через UI")
	}

	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"enabled": body.Enabled,
		"message": "Proxy Guard настройка обновлена",
	})
}

// handleSetAutorun POST /api/settings/autorun
// Body: {"enabled": true|false}
func (h *SettingsHandlers) handleSetAutorun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.server.respondError(w, http.StatusBadRequest, "invalid body")
		return
	}

	var err error
	if body.Enabled {
		err = autorun.Enable()
	} else {
		err = autorun.Disable()
	}
	if err != nil {
		h.server.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.server.respondJSON(w, http.StatusOK, SettingsResponse{Autorun: autorun.IsEnabled()})
}

// handleSetStartupProxy POST /api/settings/startup-proxy
// Body: {"enabled": true|false}
func (h *SettingsHandlers) handleSetStartupProxy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.server.respondError(w, http.StatusBadRequest, "invalid body")
		return
	}

	settings, err := config.LoadAppSettings(config.AppSettingsFile)
	if err != nil {
		h.server.logger.Warn("handleSetStartupProxy: LoadAppSettings: %v", err)
		settings = config.DefaultAppSettings()
	}
	settings.StartProxyOnLaunch = body.Enabled
	if err := config.SaveAppSettings(config.AppSettingsFile, settings); err != nil {
		h.server.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"enabled": body.Enabled,
		"message": "настройка запуска прокси обновлена",
	})
}

// handleSetKillSwitch POST /api/settings/killswitch
// Body: {"enabled": true|false}
// Примечание: настройка сохраняется в памяти и читается при старте приложения.
// При enabled=false — только снимает текущую блокировку.
// Постоянное включение KS хранится в AppConfig (устанавливается через main.go флаг).
func (h *SettingsHandlers) handleSetKillSwitch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.server.respondError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Enabled {
		// Активируем немедленно с пустым allowIP — пользователь сам управляет
		killswitch.Enable("", h.server.logger)
	} else {
		killswitch.Disable(h.server.logger)
	}
	h.server.respondJSON(w, http.StatusOK, SettingsResponse{
		Autorun:    autorun.IsEnabled(),
		KillSwitch: killswitch.IsEnabled(),
	})
}

// B-7: handleGetDNS GET /api/settings/dns — получить текущую DNS конфигурацию
// FIX Bug2: читаем из in-memory tunHandlers.routing, а не с диска.
// Чтение с диска могло вернуть устаревшее состояние если конкурентная горутина
// (handleAddRule и др.) обновила routing в памяти но ещё не сохранила на диск.
// Когда tunHandlers не инициализирован (старт приложения) — fallback на диск.
func (h *SettingsHandlers) handleGetDNS(w http.ResponseWriter, _ *http.Request) {
	var dnsConfig *config.DNSConfig

	if h.server.tunHandlers != nil {
		h.server.tunHandlers.mu.RLock()
		dnsConfig = h.server.tunHandlers.routing.DNS
		h.server.tunHandlers.mu.RUnlock()
	} else {
		routingConfigPath := config.DataDir + "/routing.json"
		cfg, err := config.LoadRoutingConfig(routingConfigPath)
		if err != nil {
			h.server.respondError(w, http.StatusInternalServerError, "ошибка чтения конфигурации: "+err.Error())
			return
		}
		dnsConfig = cfg.DNS
	}

	if dnsConfig == nil {
		dnsConfig = config.DefaultDNSConfig()
	}

	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"remote_dns": dnsConfig.RemoteDNS,
		"direct_dns": dnsConfig.DirectDNS,
	})
}

// B-7: handleSetDNS POST /api/settings/dns — обновить DNS конфигурацию
// Body: {"remote_dns": "https://1.1.1.1/dns-query", "direct_dns": "udp://8.8.8.8"}
// Разрешённые схемы: https://, tls://, udp://, tcp://, quic://
func (h *SettingsHandlers) handleSetDNS(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RemoteDNS string `json:"remote_dns"`
		DirectDNS string `json:"direct_dns"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.server.respondError(w, http.StatusBadRequest, "неверный JSON: "+err.Error())
		return
	}

	// B-7: валидируем DNS URL (разрешённые схемы)
	if err := validateDNSURL(body.RemoteDNS); err != nil {
		h.server.respondError(w, http.StatusBadRequest, "невалидный Remote DNS: "+err.Error())
		return
	}
	if err := validateDNSURL(body.DirectDNS); err != nil {
		h.server.respondError(w, http.StatusBadRequest, "невалидный Direct DNS: "+err.Error())
		return
	}

	routingConfigPath := config.DataDir + "/routing.json"
	newDNS := &config.DNSConfig{
		RemoteDNS: body.RemoteDNS,
		DirectDNS: body.DirectDNS,
	}
	// Если клиент передал пустую строку — используем значение по умолчанию.
	defaultDNS := config.DefaultDNSConfig()
	if newDNS.RemoteDNS == "" {
		newDNS.RemoteDNS = defaultDNS.RemoteDNS
	}
	if newDNS.DirectDNS == "" {
		newDNS.DirectDNS = defaultDNS.DirectDNS
	}

	if h.server.tunHandlers != nil {
		// FIX Bug2: работаем через in-memory tunHandlers.routing под мьютексом.
		// Чтение с диска + запись на диск создавало race с конкурентными handleAddRule и др.:
		// если те уже изменили routing.Rules в памяти но ещё не сохранили на диск,
		// handleSetDNS читал устаревший диск и перезаписывал его, теряя новые правила.
		h.server.tunHandlers.mu.Lock()
		oldDNS := h.server.tunHandlers.routing.DNS
		h.server.tunHandlers.routing.DNS = newDNS
		// Снимаем копию всего routing для сохранения на диск — включает все актуальные правила.
		routingSnapshot := *h.server.tunHandlers.routing
		h.server.tunHandlers.mu.Unlock()

		if err := config.SaveRoutingConfig(routingConfigPath, &routingSnapshot); err != nil {
			// Откатываем in-memory изменение при ошибке записи.
			h.server.tunHandlers.mu.Lock()
			h.server.tunHandlers.routing.DNS = oldDNS
			h.server.tunHandlers.mu.Unlock()
			h.server.respondError(w, http.StatusInternalServerError, "ошибка сохранения: "+err.Error())
			return
		}
	} else {
		// Fallback: TUN ещё не инициализирован — читаем с диска, обновляем DNS, сохраняем.
		// В этом состоянии нет конкурентных горутин изменяющих routing, race невозможен.
		cfg, err := config.LoadRoutingConfig(routingConfigPath)
		if err != nil {
			h.server.respondError(w, http.StatusInternalServerError, "ошибка чтения конфигурации: "+err.Error())
			return
		}
		cfg.DNS = newDNS
		if err := config.SaveRoutingConfig(routingConfigPath, cfg); err != nil {
			h.server.respondError(w, http.StatusInternalServerError, "ошибка сохранения: "+err.Error())
			return
		}
	}

	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"message":    "DNS конфигурация обновлена",
		"remote_dns": newDNS.RemoteDNS, // реально сохранённое значение (с дефолтом при пустой строке)
		"direct_dns": newDNS.DirectDNS,
	})

	// Применяем новый DNS сразу — sing-box должен использовать новые серверы.
	if h.server.tunHandlers != nil {
		if err := h.server.tunHandlers.TriggerApply(); err != nil {
			h.server.logger.Warn("handleSetDNS: TriggerApply: %v", err)
		}
	}
}

// B-10: handleGetGeositeUpdate GET /api/settings/geosite-update — статус и настройки автообновления geosite
func (h *SettingsHandlers) handleGetGeositeUpdate(w http.ResponseWriter, _ *http.Request) {
	s := loadGeoAutoUpdateSettings()
	meta := geositeUpdateMeta{}
	if g := h.server.GetGeoAutoUpdater(); g != nil {
		meta = g.LoadMeta()
	}
	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"enabled":       s.Enabled,
		"interval_days": s.IntervalDays,
		"last_updated":  meta.LastUpdated,
		"last_checked":  meta.LastChecked,
		"running":       h.server.GetGeoAutoUpdater() != nil && h.server.GetGeoAutoUpdater().IsRunning(),
	})
}

// B-10 / БАГ 10: handleSetGeositeUpdate POST /api/settings/geosite-update — изменить настройки автообновления geosite
// Body: {"enabled": true, "interval_days": 7}
// Применяет изменения немедленно:
//   - enabled=false  → останавливает updater
//   - enabled=true   → запускает/перезапускает updater (в т.ч. при смене интервала)
func (h *SettingsHandlers) handleSetGeositeUpdate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled      bool `json:"enabled"`
		IntervalDays int  `json:"interval_days"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.server.respondError(w, http.StatusBadRequest, "неверный JSON: "+err.Error())
		return
	}
	if body.IntervalDays <= 0 {
		body.IntervalDays = 7
	}

	// БАГ 10: читаем старый интервал ДО сохранения чтобы понять изменился ли он.
	oldSettings := loadGeoAutoUpdateSettings()

	s := geoAutoUpdateSettings{Enabled: body.Enabled, IntervalDays: body.IntervalDays}
	if err := saveGeoAutoUpdateSettings(s); err != nil {
		h.server.respondError(w, http.StatusInternalServerError, "ошибка сохранения настроек: "+err.Error())
		return
	}

	intervalChanged := body.IntervalDays != oldSettings.IntervalDays
	newInterval := time.Duration(body.IntervalDays) * 24 * time.Hour

	g := h.server.GetGeoAutoUpdater()
	if !body.Enabled {
		// Выключаем updater если запущен.
		if g != nil && g.IsRunning() {
			g.Stop()
		}
	} else {
		// Включаем или перезапускаем с новым интервалом.
		if g == nil || !g.IsRunning() {
			// Создаём новый updater и запускаем.
			newG := NewGeoAutoUpdater(h.server.logger, newInterval)
			h.server.SetGeoAutoUpdater(newG)
			newG.Start(h.server.lifecycleCtx)
		} else if intervalChanged {
			// Интервал изменился — нельзя поменять на ходу, нужен пересоздать.
			g.Stop()
			newG := NewGeoAutoUpdater(h.server.logger, newInterval)
			h.server.SetGeoAutoUpdater(newG)
			newG.Start(h.server.lifecycleCtx)
		}
	}

	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"enabled":       body.Enabled,
		"interval_days": body.IntervalDays,
		"applied":       true,
	})
}

// B-7: validateDNSURL проверяет что DNS URL имеет разрешённую схему.
// Разрешённые: https://, tls://, udp://, tcp://, quic://
// Пустая строка допустима — означает «использовать значение по умолчанию».
func validateDNSURL(url string) error {
	if url == "" {
		return nil // пустая строка = "использовать значение по умолчанию"
	}

	allowedSchemes := []string{"https://", "tls://", "udp://", "tcp://", "quic://"}
	allowed := false
	for _, scheme := range allowedSchemes {
		if strings.HasPrefix(url, scheme) {
			allowed = true
			break
		}
	}

	if !allowed {
		return fmt.Errorf("разрешённые схемы: https://, tls://, udp://, tcp://, quic://")
	}

	return nil
}
