//go:build !windows

package api

import "net/http"

// handleProcIcon — заглушка для не-Windows платформ.
// На Windows иконки извлекаются через Shell API (procicon_handler_windows.go).
func (s *Server) handleProcIcon(w http.ResponseWriter, r *http.Request) {
	http.NotFound(w, r)
}
