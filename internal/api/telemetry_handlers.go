package api

import (
	"context"
	"net/http"
	"path/filepath"
	"time"

	"proxyclient/internal/config"
	"proxyclient/internal/telemetry"
)

const telemetryProxyTimeout = 20 * time.Second

func SetupTelemetryRoutes(s *Server) {
	api := s.router.PathPrefix("/api").Subrouter()
	api.HandleFunc("/telemetry/export", s.handleTelemetryExport).Methods("GET", "OPTIONS")
	api.HandleFunc("/telemetry/delete", s.handleTelemetryDelete).Methods("POST", "OPTIONS")
}

func (s *Server) handleTelemetryExport(w http.ResponseWriter, r *http.Request) {
	client := telemetryClientFromSettings()
	ctx, cancel := context.WithTimeout(r.Context(), telemetryProxyTimeout)
	defer cancel()
	data, err := client.ExportData(ctx)
	if err != nil {
		s.respondError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) handleTelemetryDelete(w http.ResponseWriter, r *http.Request) {
	client := telemetryClientFromSettings()
	ctx, cancel := context.WithTimeout(r.Context(), telemetryProxyTimeout)
	defer cancel()
	if err := client.DeleteData(ctx); err != nil {
		s.respondError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.respondJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func telemetryClientFromSettings() telemetry.Client {
	settings, err := config.LoadAppSettings(config.AppSettingsFile)
	if err != nil {
		settings = config.DefaultAppSettings()
	}
	return telemetry.Client{
		Enabled:       settings.Telemetry.Enabled,
		BaseURL:       settings.Telemetry.BaseURL,
		AnonymousPath: filepath.Join(config.DataDir, "telemetry_id"),
		UserAgent:     "SafeSky-Telemetry/1",
	}
}
