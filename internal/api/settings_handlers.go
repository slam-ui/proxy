package api

import (
	"encoding/json"
	"net/http"

	"proxyclient/internal/autorun"
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
}

// SettingsResponse — состояние всех настроек приложения.
type SettingsResponse struct {
	Autorun     bool `json:"autorun"`      // включён ли автозапуск при входе в Windows
	KillSwitch  bool `json:"kill_switch"`  // активен ли Kill Switch прямо сейчас
}

// handleGetSettings GET /api/settings
func (h *SettingsHandlers) handleGetSettings(w http.ResponseWriter, _ *http.Request) {
	h.server.respondJSON(w, http.StatusOK, SettingsResponse{
		Autorun:    autorun.IsEnabled(),
		KillSwitch: killswitch.IsEnabled(),
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
