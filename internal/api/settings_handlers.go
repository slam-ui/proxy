package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"proxyclient/internal/autorun"
	"proxyclient/internal/config"
	"proxyclient/internal/hotkeys"
	"proxyclient/internal/i18n"
	"proxyclient/internal/killswitch"
	"proxyclient/internal/logger"
	"proxyclient/internal/tray"
)

const (
	maxSettingsRequestBytes      = 64 << 10
	maxSettingsSmallRequestBytes = 4 << 10
)

func (h *SettingsHandlers) decodeRequest(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64, errorMessage string, includeDetails bool) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		h.server.respondError(w, http.StatusBadRequest, formatDecodeError(errorMessage, err, includeDetails))
		return false
	}
	var extra struct{}
	if err := dec.Decode(&extra); err == nil {
		h.server.respondError(w, http.StatusBadRequest, formatDecodeError(errorMessage, errors.New("multiple JSON values"), includeDetails))
		return false
	} else if !errors.Is(err, io.EOF) {
		h.server.respondError(w, http.StatusBadRequest, formatDecodeError(errorMessage, err, includeDetails))
		return false
	}
	return true
}

func formatDecodeError(message string, err error, includeDetails bool) string {
	if includeDetails {
		return message + ": " + err.Error()
	}
	return message
}

// SettingsHandlers обработчики настроек приложения.
// Использует Server для доступа к respondJSON/respondError — согласованно с TunHandlers.
type SettingsHandlers struct {
	server *Server
}

// SetupSettingsRoutes регистрирует API для настроек приложения.
func SetupSettingsRoutes(s *Server) {
	h := &SettingsHandlers{server: s}
	s.router.HandleFunc("/api/settings", h.handleGetSettings).Methods("GET", "OPTIONS")
	s.router.HandleFunc("/api/settings", h.handleSetSettings).Methods("POST", "OPTIONS")
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

// handleSetSettings POST /api/settings updates lifecycle settings that are safe
// to change without rebuilding routing rules.
func (h *SettingsHandlers) handleSetSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ReconnectIntervalMin *int                              `json:"reconnect_interval_min"`
		CloseToTray          *bool                             `json:"close_to_tray"`
		Language             *string                           `json:"language"`
		KeepaliveEnabled     *bool                             `json:"keepalive_enabled"`
		KeepaliveIntervalSec *int                              `json:"keepalive_interval_sec"`
		Schedule             *config.Schedule                  `json:"schedule"`
		MemoryLimitMB        *uint64                           `json:"memory_limit_mb"`
		StartProxyOnLaunch   *bool                             `json:"start_proxy_on_launch"`
		ManualSingBoxConfig  *bool                             `json:"manual_singbox_config"`
		SmartFailover        *config.SmartFailoverSettings     `json:"smart_failover"`
		DNSGuard             *config.DNSGuardSettings          `json:"dns_guard"`
		NetworkProtection    *config.NetworkProtectionSettings `json:"network_protection"`
		TrafficBudget        *config.TrafficBudgetSettings     `json:"traffic_budget"`
		Updates              *config.UpdateSettings            `json:"updates"`
		LeakTest             *config.LeakTestSettings          `json:"leak_test"`
		Hotkeys              *config.HotkeySettings            `json:"hotkeys"`
	}
	if !h.decodeRequest(w, r, &body, maxSettingsRequestBytes, "invalid body", false) {
		return
	}

	settings, err := config.LoadAppSettings(config.AppSettingsFile)
	if err != nil {
		h.server.logger.Warn("handleSetSettings: LoadAppSettings: %v", err)
		settings = config.DefaultAppSettings()
	}
	if body.ReconnectIntervalMin != nil {
		if *body.ReconnectIntervalMin < 0 {
			h.server.respondError(w, http.StatusBadRequest, "reconnect_interval_min must be >= 0")
			return
		}
		settings.ReconnectIntervalMin = *body.ReconnectIntervalMin
	}
	if body.CloseToTray != nil {
		settings.CloseToTray = *body.CloseToTray
	}
	if body.Language != nil {
		switch *body.Language {
		case "ru", "en", "system":
			settings.Language = *body.Language
		default:
			h.server.respondError(w, http.StatusBadRequest, "language: ru | en | system")
			return
		}
	}
	if body.KeepaliveEnabled != nil {
		settings.KeepaliveEnabled = *body.KeepaliveEnabled
	}
	if body.KeepaliveIntervalSec != nil {
		if *body.KeepaliveIntervalSec < 30 {
			h.server.respondError(w, http.StatusBadRequest, "keepalive_interval_sec must be >= 30")
			return
		}
		settings.KeepaliveIntervalSec = *body.KeepaliveIntervalSec
	}
	if body.Schedule != nil {
		settings.Schedule = *body.Schedule
	}
	if body.MemoryLimitMB != nil {
		settings.MemoryLimitMB = *body.MemoryLimitMB
	}
	if body.StartProxyOnLaunch != nil {
		settings.StartProxyOnLaunch = *body.StartProxyOnLaunch
	}
	if body.ManualSingBoxConfig != nil {
		settings.ManualSingBoxConfig = *body.ManualSingBoxConfig
	}
	if body.SmartFailover != nil {
		settings.SmartFailover = *body.SmartFailover
	}
	if body.DNSGuard != nil {
		settings.DNSGuard = *body.DNSGuard
	}
	if body.NetworkProtection != nil {
		settings.NetworkProtection = *body.NetworkProtection
	}
	if body.TrafficBudget != nil {
		settings.TrafficBudget = *body.TrafficBudget
	}
	if body.Updates != nil {
		settings.Updates = *body.Updates
	}
	if body.LeakTest != nil {
		settings.LeakTest = *body.LeakTest
	}
	if body.Hotkeys != nil {
		normalized, err := normalizeHotkeySettings(*body.Hotkeys)
		if err != nil {
			h.server.respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		settings.Hotkeys = normalized
	}
	hotkeysChanged := body.Hotkeys != nil
	if err := config.SaveAppSettings(config.AppSettingsFile, settings); err != nil {
		h.server.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if settings.ReconnectIntervalMin > 0 {
		h.server.StartPeriodicReconnect(time.Duration(settings.ReconnectIntervalMin) * time.Minute)
	} else {
		h.server.StopPeriodicReconnect()
	}
	if h.server.config.CloseToTrayFn != nil {
		h.server.config.CloseToTrayFn(settings.CloseToTray)
	}
	if body.Language != nil {
		tray.SetLanguage(settings.Language)
	}
	conflicts := h.currentHotkeyConflicts(settings.Hotkeys, hotkeysChanged)
	h.server.respondJSON(w, http.StatusOK, settingsResponseWithHotkeyConflicts{
		AppSettings:     settings,
		HotkeyConflicts: conflicts,
	})
}

type settingsResponseWithHotkeyConflicts struct {
	config.AppSettings
	HotkeyConflicts []hotkeys.Conflict `json:"hotkey_conflicts,omitempty"`
}

// SettingsResponse — состояние всех настроек приложения.
type SettingsResponse struct {
	Autorun              bool                             `json:"autorun"`               // включён ли автозапуск при входе в Windows
	KillSwitch           bool                             `json:"kill_switch"`           // активен ли Kill Switch прямо сейчас
	KillSwitchState      killswitch.State                 `json:"kill_switch_state"`     // persisted fail-close state
	ProxyGuard           bool                             `json:"proxy_guard"`           // B-2: активна ли Proxy Guard для восстановления
	StartProxyOnLaunch   bool                             `json:"start_proxy_on_launch"` // включать прокси сразу после запуска клиента
	CloseToTray          bool                             `json:"close_to_tray"`         // закрытие окна сворачивает в трей вместо выхода
	Language             string                           `json:"language"`
	EffectiveLanguage    i18n.Locale                      `json:"effective_language"`
	ReconnectIntervalMin int                              `json:"reconnect_interval_min"`
	KeepaliveEnabled     bool                             `json:"keepalive_enabled"`
	KeepaliveIntervalSec int                              `json:"keepalive_interval_sec"`
	Schedule             config.Schedule                  `json:"schedule"`
	MemoryLimitMB        uint64                           `json:"memory_limit_mb"`
	ManualSingBoxConfig  bool                             `json:"manual_singbox_config"`
	SmartFailover        config.SmartFailoverSettings     `json:"smart_failover"`
	DNSGuard             config.DNSGuardSettings          `json:"dns_guard"`
	NetworkProtection    config.NetworkProtectionSettings `json:"network_protection"`
	TrafficBudget        config.TrafficBudgetSettings     `json:"traffic_budget"`
	Updates              config.UpdateSettings            `json:"updates"`
	LeakTest             config.LeakTestSettings          `json:"leak_test"`
	Hotkeys              config.HotkeySettings            `json:"hotkeys"`
	HotkeyConflicts      []hotkeys.Conflict               `json:"hotkey_conflicts,omitempty"`
}

// handleGetSettings GET /api/settings
func (h *SettingsHandlers) handleGetSettings(w http.ResponseWriter, _ *http.Request) {
	appSettings, err := config.LoadAppSettings(config.AppSettingsFile)
	if err != nil {
		h.server.logger.Warn("handleGetSettings: %v", err)
		appSettings = config.DefaultAppSettings()
	}
	h.server.respondJSON(w, http.StatusOK, SettingsResponse{
		Autorun:              autorun.IsEnabled(),
		KillSwitch:           killswitch.IsEnabled(),
		KillSwitchState:      loadKillSwitchStateForResponse(h.server.logger),
		ProxyGuard:           h.server.IsProxyGuardEnabled(),
		StartProxyOnLaunch:   appSettings.StartProxyOnLaunch,
		CloseToTray:          appSettings.CloseToTray,
		Language:             appSettings.Language,
		EffectiveLanguage:    i18n.EffectiveLocale(appSettings.Language),
		ReconnectIntervalMin: appSettings.ReconnectIntervalMin,
		KeepaliveEnabled:     appSettings.KeepaliveEnabled,
		KeepaliveIntervalSec: appSettings.KeepaliveIntervalSec,
		Schedule:             appSettings.Schedule,
		MemoryLimitMB:        appSettings.MemoryLimitMB,
		ManualSingBoxConfig:  appSettings.ManualSingBoxConfig,
		SmartFailover:        appSettings.SmartFailover,
		DNSGuard:             appSettings.DNSGuard,
		NetworkProtection:    appSettings.NetworkProtection,
		TrafficBudget:        appSettings.TrafficBudget,
		Updates:              appSettings.Updates,
		LeakTest:             appSettings.LeakTest,
		Hotkeys:              appSettings.Hotkeys,
		HotkeyConflicts:      h.currentHotkeyConflicts(appSettings.Hotkeys, false),
	})
}

func (h *SettingsHandlers) currentHotkeyConflicts(settings config.HotkeySettings, changed bool) []hotkeys.Conflict {
	if changed && h.server.config.HotkeysUpdatedFn != nil {
		return h.server.config.HotkeysUpdatedFn(settings)
	}
	if h.server.config.HotkeyConflictsFn != nil {
		return h.server.config.HotkeyConflictsFn()
	}
	return nil
}

func normalizeHotkeySettings(settings config.HotkeySettings) (config.HotkeySettings, error) {
	in := hotkeys.Settings{Enabled: settings.Enabled, Bindings: make([]hotkeys.Binding, 0, len(settings.Bindings))}
	for _, binding := range settings.Bindings {
		if binding.Accelerator != "" {
			if _, err := hotkeys.ParseAccelerator(binding.Accelerator); err != nil {
				return config.HotkeySettings{}, fmt.Errorf("hotkeys.%s: %w", binding.Action, err)
			}
		}
		in.Bindings = append(in.Bindings, hotkeys.Binding{
			Action:      hotkeys.Action(binding.Action),
			Accelerator: binding.Accelerator,
			Enabled:     binding.Enabled,
		})
	}
	normalized := hotkeys.NormalizeSettings(in)
	out := config.HotkeySettings{Enabled: normalized.Enabled, Bindings: make([]config.HotkeyBinding, 0, len(normalized.Bindings))}
	for _, binding := range normalized.Bindings {
		out.Bindings = append(out.Bindings, config.HotkeyBinding{
			Action:      string(binding.Action),
			Accelerator: binding.Accelerator,
			Enabled:     binding.Enabled,
		})
	}
	return out, nil
}

func loadKillSwitchStateForResponse(log logger.Logger) killswitch.State {
	st, err := killswitch.LoadState()
	if err != nil && log != nil {
		log.Warn("handleGetSettings: killswitch state: %v", err)
	}
	return st
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
	if !h.decodeRequest(w, r, &body, maxSettingsSmallRequestBytes, "invalid body", false) {
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
	if !h.decodeRequest(w, r, &body, maxSettingsSmallRequestBytes, "invalid body", false) {
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
	if !h.decodeRequest(w, r, &body, maxSettingsSmallRequestBytes, "invalid body", false) {
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
	if !h.decodeRequest(w, r, &body, maxSettingsSmallRequestBytes, "invalid body", false) {
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
		"remote_dns":          dnsConfig.RemoteDNS,
		"direct_dns":          dnsConfig.DirectDNS,
		"remote_dns_fallback": dnsConfig.RemoteDNSFallback,
	})
}

// B-7: handleSetDNS POST /api/settings/dns — обновить DNS конфигурацию
// Body: {"remote_dns": "https://1.1.1.1/dns-query", "direct_dns": "udp://8.8.8.8"}
// Разрешённые схемы: https://, tls://, udp://, tcp://, quic://
func (h *SettingsHandlers) handleSetDNS(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RemoteDNS         string   `json:"remote_dns"`
		DirectDNS         string   `json:"direct_dns"`
		RemoteDNSFallback []string `json:"remote_dns_fallback"`
	}
	if !h.decodeRequest(w, r, &body, maxSettingsRequestBytes, "неверный JSON", true) {
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
		RemoteDNS:         body.RemoteDNS,
		DirectDNS:         body.DirectDNS,
		RemoteDNSFallback: body.RemoteDNSFallback,
	}
	// Если клиент передал пустую строку — используем значение по умолчанию.
	defaultDNS := config.DefaultDNSConfig()
	if newDNS.RemoteDNS == "" {
		newDNS.RemoteDNS = defaultDNS.RemoteDNS
	}
	if newDNS.DirectDNS == "" {
		newDNS.DirectDNS = defaultDNS.DirectDNS
	}
	if len(newDNS.RemoteDNSFallback) == 0 {
		newDNS.RemoteDNSFallback = defaultDNS.RemoteDNSFallback
	}

	applyAlreadyRequested := false
	if h.server.tunHandlers != nil {
		// FIX Bug2: работаем через общий serialized read-modify-write routing.
		// Без routingOpMu конкурентные handleAddRule/import/bulk replace могли сохранить
		// снимок без нового DNS или, наоборот, handleSetDNS мог потерять только что добавленные правила.
		if err := h.server.mutateRoutingSnapshot(func(routing *config.RoutingConfig) (bool, error) {
			routing.DNS = newDNS
			return true, nil
		}); err != nil {
			h.server.respondError(w, http.StatusInternalServerError, "ошибка сохранения: "+err.Error())
			return
		}
		applyAlreadyRequested = true
	} else {
		// Fallback: TUN ещё не инициализирован — читаем с диска, обновляем DNS, сохраняем.
		// В этом состоянии нет конкурентных горутин изменяющих routing, race невозможен.
		h.server.routingOpMu.Lock()
		cfg, err := config.LoadRoutingConfig(routingConfigPath)
		if err != nil {
			h.server.routingOpMu.Unlock()
			h.server.respondError(w, http.StatusInternalServerError, "ошибка чтения конфигурации: "+err.Error())
			return
		}
		cfg.DNS = newDNS
		if err := config.SaveRoutingConfig(routingConfigPath, cfg); err != nil {
			h.server.routingOpMu.Unlock()
			h.server.respondError(w, http.StatusInternalServerError, "ошибка сохранения: "+err.Error())
			return
		}
		h.server.routingOpMu.Unlock()
	}

	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"message":             "DNS конфигурация обновлена",
		"remote_dns":          newDNS.RemoteDNS,
		"direct_dns":          newDNS.DirectDNS,
		"remote_dns_fallback": newDNS.RemoteDNSFallback,
	})

	// Применяем новый DNS сразу — sing-box должен использовать новые серверы.
	if h.server.tunHandlers != nil && !applyAlreadyRequested {
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
	if !h.decodeRequest(w, r, &body, maxSettingsSmallRequestBytes, "неверный JSON", true) {
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
