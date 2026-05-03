package api

import (
	"net/http"

	"proxyclient/internal/i18n"
)

func SetupI18nRoutes(s *Server) {
	api := s.router.PathPrefix("/api").Subrouter()
	api.HandleFunc("/i18n/messages", s.handleI18nMessages).Methods("GET", "OPTIONS")
}

func (s *Server) handleI18nMessages(w http.ResponseWriter, r *http.Request) {
	locale := i18n.NormalizeLocale(i18n.Locale(r.URL.Query().Get("locale")))
	msgs, err := i18n.LoadMessages()
	if err != nil {
		s.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.respondJSON(w, http.StatusOK, map[string]any{
		"locale":   locale,
		"messages": msgs[locale],
	})
}
