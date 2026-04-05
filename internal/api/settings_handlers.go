package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

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
	Autorun    bool `json:"autorun"`     // включён ли автозапуск при входе в Windows
	KillSwitch bool `json:"kill_switch"` // активен ли Kill Switch прямо сейчас
	ProxyGuard bool `json:"proxy_guard"` // B-2: активна ли Proxy Guard для восстановления
}

// handleGetSettings GET /api/settings
func (h *SettingsHandlers) handleGetSettings(w http.ResponseWriter, _ *http.Request) {
	h.server.respondJSON(w, http.StatusOK, SettingsResponse{
		Autorun:    autorun.IsEnabled(),
		KillSwitch: killswitch.IsEnabled(),
		ProxyGuard: h.server.IsProxyGuardEnabled(),
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
// B-2: Примечание: фактическое запущение/остановка горутины guard происходит на уровне App.
// Этот хендлер только обновляет флаг. App периодически синхронизирует состояние.
func (h *SettingsHandlers) handleSetProxyGuard(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.server.respondError(w, http.StatusBadRequest, "invalid body")
		return
	}

	h.server.SetProxyGuardEnabled(body.Enabled)

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
func (h *SettingsHandlers) handleGetDNS(w http.ResponseWriter, _ *http.Request) {
	routingConfigPath := config.DataDir + "/routing.json"
	cfg, err := config.LoadRoutingConfig(routingConfigPath)
	if err != nil {
		h.server.respondError(w, http.StatusInternalServerError, "ошибка чтения конфигурации: "+err.Error())
		return
	}

	dnsConfig := cfg.DNS
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

	// Загружаем текущую конфиг, обновляем DNS, сохраняем
	routingConfigPath := config.DataDir + "/routing.json"
	cfg, err := config.LoadRoutingConfig(routingConfigPath)
	if err != nil {
		h.server.respondError(w, http.StatusInternalServerError, "ошибка чтения конфигурации: "+err.Error())
		return
	}

	cfg.DNS = &config.DNSConfig{
		RemoteDNS: body.RemoteDNS,
		DirectDNS: body.DirectDNS,
	}

	if err := config.SaveRoutingConfig(routingConfigPath, cfg); err != nil {
		h.server.respondError(w, http.StatusInternalServerError, "ошибка сохранения: "+err.Error())
		return
	}

	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"message":    "DNS конфигурация обновлена",
		"remote_dns": body.RemoteDNS,
		"direct_dns": body.DirectDNS,
	})
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

// B-10: handleSetGeositeUpdate POST /api/settings/geosite-update — изменить настройки автообновления geosite
// Body: {"enabled": true, "interval_days": 7}
// При enabled=false останавливает updater; при enabled=true запускает (если ещё не запущен).
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

	s := geoAutoUpdateSettings{Enabled: body.Enabled, IntervalDays: body.IntervalDays}
	if err := saveGeoAutoUpdateSettings(s); err != nil {
		h.server.respondError(w, http.StatusInternalServerError, "ошибка сохранения настроек: "+err.Error())
		return
	}

	g := h.server.GetGeoAutoUpdater()
	if g != nil {
		if body.Enabled && !g.IsRunning() {
			g.Start(h.server.lifecycleCtx)
		} else if !body.Enabled && g.IsRunning() {
			g.Stop()
		}
	}

	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"enabled":       body.Enabled,
		"interval_days": body.IntervalDays,
		"message":       "настройки автообновления geosite обновлены",
	})
}

// B-7: validateDNSURL проверяет что DNS URL имеет разрешённую схему.
// Разрешённые: https://, tls://, udp://, tcp://, quic://
func validateDNSURL(url string) error {
	if url == "" {
		return fmt.Errorf("DNS адрес не должен быть пуст")
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
