package api

import (
	"encoding/json"
	"net/http"

	"proxyclient/internal/autorun"
)

// SetupSettingsRoutes регистрирует API для настроек приложения.
func SetupSettingsRoutes(s *Server) {
	s.router.HandleFunc("/api/settings", handleGetSettings).Methods("GET", "OPTIONS")
	s.router.HandleFunc("/api/settings/autorun", handleSetAutorun).Methods("POST", "OPTIONS")
}

// SettingsResponse — состояние всех настроек приложения.
type SettingsResponse struct {
	Autorun bool `json:"autorun"` // включён ли автозапуск при входе в Windows
}

// handleGetSettings GET /api/settings
func handleGetSettings(w http.ResponseWriter, _ *http.Request) {
	resp := SettingsResponse{
		Autorun: autorun.IsEnabled(),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleSetAutorun POST /api/settings/autorun
// Body: {"enabled": true|false}
func handleSetAutorun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}

	var err error
	if body.Enabled {
		err = autorun.Enable()
	} else {
		err = autorun.Disable()
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(SettingsResponse{Autorun: autorun.IsEnabled()})
}
