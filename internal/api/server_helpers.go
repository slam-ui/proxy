package api

import (
	"net/http"
	"strings"
	"time"
)

const (
	defaultMutationRatePerSecond = 5
	maxRequestBodyBytes          = 2 << 20 // 2 MB
	maxBackupFileBytes           = 5 << 20 // 5 MB
	maxMultipartOverheadBytes    = 32 << 10
	maxBackupRequestBodyBytes    = maxBackupFileBytes + maxMultipartOverheadBytes
	quitSignalDelay              = 100 * time.Millisecond
	slowRequestThreshold         = 200 * time.Millisecond
)

func isMutationMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch:
		return true
	default:
		return false
	}
}

func isRequestBodyLimitedMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		return true
	default:
		return false
	}
}

func maxRequestBytesForPath(path string) int64 {
	switch path {
	case "/api/backup/import", "/api/backup/restore":
		return maxBackupRequestBodyBytes
	default:
		return maxRequestBodyBytes
	}
}

func isAllowedCORSOrigin(origin string) bool {
	switch origin {
	case "", "http://localhost:8080", "http://127.0.0.1:8080", "app://":
		return true
	default:
		return false
	}
}

func isStaticAssetPath(path string) bool {
	switch {
	case strings.HasSuffix(path, ".js"):
		return true
	case strings.HasSuffix(path, ".css"):
		return true
	case strings.HasSuffix(path, ".ico"):
		return true
	case strings.HasSuffix(path, ".html"):
		return true
	default:
		return false
	}
}

func buildSilentPathCache(extra []string) map[string]bool {
	m := map[string]bool{
		"/api/status":           true,
		"/api/health":           true,
		"/api/tun/apply/status": true,
		"/api/events":           true,
		"/api/events/clear":     true,
		"/api/geoip":            true,
	}
	for _, p := range extra {
		m[p] = true
	}
	return m
}

func (s *Server) getSilentPathCache() map[string]bool {
	s.silentMu.RLock()
	cache := s.silentCache
	s.silentMu.RUnlock()
	if cache != nil {
		return cache
	}

	s.silentMu.Lock()
	defer s.silentMu.Unlock()
	if s.silentCache == nil {
		s.silentCache = buildSilentPathCache(s.config.SilentPaths)
	}
	return s.silentCache
}
