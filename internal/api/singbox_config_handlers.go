package api

import (
	"encoding/json"
	"net/http"
	"os"
	"time"

	"proxyclient/internal/config"
	"proxyclient/internal/fileutil"
)

type singBoxConfigResponse struct {
	Path          string `json:"path"`
	Content       string `json:"content"`
	ManualEnabled bool   `json:"manual_enabled"`
	Exists        bool   `json:"exists"`
	UpdatedAt     int64  `json:"updated_at,omitempty"`
}

func (s *Server) handleGetSingBoxConfig(w http.ResponseWriter, _ *http.Request) {
	path := s.config.ConfigPath
	settings, _ := config.LoadAppSettings(config.AppSettingsFile)

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			s.respondJSON(w, http.StatusOK, singBoxConfigResponse{
				Path:          path,
				ManualEnabled: settings.ManualSingBoxConfig,
				Exists:        false,
			})
			return
		}
		s.respondError(w, http.StatusInternalServerError, "не удалось прочитать config.singbox.json: "+err.Error())
		return
	}

	var updatedAt int64
	if st, statErr := os.Stat(path); statErr == nil {
		updatedAt = st.ModTime().Unix()
	}
	s.respondJSON(w, http.StatusOK, singBoxConfigResponse{
		Path:          path,
		Content:       string(data),
		ManualEnabled: settings.ManualSingBoxConfig,
		Exists:        true,
		UpdatedAt:     updatedAt,
	})
}

func (s *Server) handleSetSingBoxConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Content       *string `json:"content"`
		ManualEnabled *bool   `json:"manual_enabled"`
		Apply         bool    `json:"apply"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.respondError(w, http.StatusBadRequest, "неверный JSON: "+err.Error())
		return
	}

	if body.Content != nil {
		data := []byte(*body.Content)
		if !json.Valid(data) {
			s.respondError(w, http.StatusBadRequest, "config.singbox.json должен быть валидным JSON")
			return
		}
		if len(data) == 0 || data[len(data)-1] != '\n' {
			data = append(data, '\n')
		}
		if err := fileutil.WriteAtomic(s.config.ConfigPath, data, 0644); err != nil {
			s.respondError(w, http.StatusInternalServerError, "не удалось сохранить config.singbox.json: "+err.Error())
			return
		}
	}

	settings, err := config.LoadAppSettings(config.AppSettingsFile)
	if err != nil {
		s.logger.Warn("handleSetSingBoxConfig: LoadAppSettings: %v", err)
		settings = config.DefaultAppSettings()
	}
	if body.ManualEnabled != nil {
		settings.ManualSingBoxConfig = *body.ManualEnabled
	} else if body.Content != nil {
		settings.ManualSingBoxConfig = true
	}
	if err := config.SaveAppSettings(config.AppSettingsFile, settings); err != nil {
		s.respondError(w, http.StatusInternalServerError, "не удалось сохранить режим ручного конфига: "+err.Error())
		return
	}

	applyErr := ""
	if body.Apply && s.tunHandlers != nil {
		if err := s.tunHandlers.TriggerApplyWithConfig(); err != nil {
			applyErr = err.Error()
			s.logger.Warn("handleSetSingBoxConfig: TriggerApplyWithConfig: %v", err)
		}
	}

	s.respondJSON(w, http.StatusOK, map[string]interface{}{
		"message":        "config.singbox.json сохранён",
		"path":           s.config.ConfigPath,
		"manual_enabled": settings.ManualSingBoxConfig,
		"updated_at":     time.Now().Unix(),
		"apply_error":    applyErr,
	})
}
