package api

import (
	"context"
	"net/http"
	"path/filepath"
	"time"

	"proxyclient/internal/config"
	"proxyclient/internal/update"
	"proxyclient/internal/version"
)

const updateOperationTimeout = 45 * time.Second

func SetupUpdateRoutes(s *Server) {
	api := s.router.PathPrefix("/api").Subrouter()
	api.HandleFunc("/update/status", s.handleUpdateStatus).Methods("GET", "OPTIONS")
	api.HandleFunc("/update/check", s.handleUpdateCheck).Methods("POST", "OPTIONS")
	api.HandleFunc("/update/install", s.handleUpdateInstall).Methods("POST", "OPTIONS")
}

func (s *Server) handleUpdateStatus(w http.ResponseWriter, _ *http.Request) {
	settings, err := config.LoadAppSettings(config.AppSettingsFile)
	if err != nil {
		s.logger.Warn("handleUpdateStatus: LoadAppSettings: %v", err)
		settings = config.DefaultAppSettings()
	}
	state, err := update.LoadState(updateStatePath())
	if err != nil {
		s.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.respondJSON(w, http.StatusOK, map[string]interface{}{
		"build":    version.Info(),
		"settings": settings.Updates,
		"state":    state,
	})
}

func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	up, err := updaterFromSettings()
	if err != nil {
		s.respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), updateOperationTimeout)
	defer cancel()
	res, err := up.CheckLatest(ctx)
	if err != nil {
		s.logger.Debug("update check failed: %v", err)
		s.respondError(w, http.StatusBadGateway, "update check failed")
		return
	}
	s.respondJSON(w, http.StatusOK, res)
}

func (s *Server) handleUpdateInstall(w http.ResponseWriter, r *http.Request) {
	up, err := updaterFromSettings()
	if err != nil {
		s.respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), updateOperationTimeout)
	defer cancel()
	res, err := up.CheckLatest(ctx)
	if err != nil {
		s.respondError(w, http.StatusBadGateway, "update check failed")
		return
	}
	if !res.UpdateAvailable || res.Latest == nil {
		s.respondJSON(w, http.StatusOK, map[string]interface{}{
			"started": false,
			"reason":  "no update available",
			"result":  res,
		})
		return
	}
	downloaded, err := up.Download(ctx, res.Latest)
	if err != nil {
		s.respondError(w, http.StatusBadGateway, err.Error())
		return
	}
	if err := up.Apply(ctx, downloaded); err != nil {
		s.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := up.LaunchInstaller(ctx, downloaded, nil); err != nil {
		s.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.respondJSON(w, http.StatusOK, map[string]interface{}{
		"started": true,
		"version": res.Latest.Version,
	})
	if s.config.QuitChan != nil {
		go func() {
			time.Sleep(200 * time.Millisecond)
			s.quitOnce.Do(func() { close(s.config.QuitChan) })
		}()
	}
}

func updaterFromSettings() (*update.Updater, error) {
	settings, err := config.LoadAppSettings(config.AppSettingsFile)
	if err != nil {
		settings = config.DefaultAppSettings()
	}
	return update.New(update.Config{
		BaseURL:        settings.Updates.BaseURL,
		Channel:        settings.Updates.Channel,
		CurrentVersion: version.Version,
		TempDir:        filepath.Join(config.DataDir, "updates"),
		StatePath:      updateStatePath(),
	})
}

func updateStatePath() string {
	return filepath.Join(config.DataDir, "update_state.json")
}
