package api

import (
	"net/http"

	"proxyclient/internal/onboarding"
)

type OnboardingHandlers struct {
	server       *Server
	markerPath   string
	settingsPath string
}

func SetupOnboardingRoutes(s *Server) {
	h := &OnboardingHandlers{server: s, markerPath: onboarding.MarkerFile}
	api := s.router.PathPrefix("/api").Subrouter()
	api.HandleFunc("/onboarding/status", h.handleStatus).Methods("GET", "OPTIONS")
	api.HandleFunc("/onboarding/complete", h.handleComplete).Methods("POST", "OPTIONS")
	api.HandleFunc("/onboarding/skip", h.handleComplete).Methods("POST", "OPTIONS")
}

func (h *OnboardingHandlers) handleStatus(w http.ResponseWriter, _ *http.Request) {
	status, err := onboarding.Current(h.markerPath)
	if err != nil {
		h.server.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.server.respondJSON(w, http.StatusOK, status)
}

func (h *OnboardingHandlers) handleComplete(w http.ResponseWriter, _ *http.Request) {
	if _, err := onboarding.ApplySmartDefaults(h.settingsPath); err != nil {
		h.server.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := onboarding.MarkComplete(h.markerPath); err != nil {
		h.server.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	status, err := onboarding.Current(h.markerPath)
	if err != nil {
		h.server.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.server.respondJSON(w, http.StatusOK, status)
}
