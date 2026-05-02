package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"time"

	"proxyclient/internal/config"
	"proxyclient/internal/ipv6mitigation"
	"proxyclient/internal/leaktest"
)

const leakTestTimeout = 15 * time.Second

func SetupLeakTestRoutes(s *Server) {
	api := s.router.PathPrefix("/api").Subrouter()
	api.HandleFunc("/leaktest/dns", s.handleDNSLeakTest).Methods("POST", "OPTIONS")
	api.HandleFunc("/leaktest/ipv6", s.handleIPv6LeakTest).Methods("POST", "OPTIONS")
	api.HandleFunc("/leaktest/ipv6/mitigation", s.handleIPv6MitigationStatus).Methods("GET", "OPTIONS")
	api.HandleFunc("/leaktest/ipv6/disable", s.handleIPv6Disable).Methods("POST", "OPTIONS")
	api.HandleFunc("/leaktest/ipv6/restore", s.handleIPv6Restore).Methods("POST", "OPTIONS")
	api.HandleFunc("/leaktest/summary", s.handleLeakTestSummary).Methods("POST", "OPTIONS")
	api.HandleFunc("/leaktest/webrtc", s.handleWebRTCTest).Methods("GET", "OPTIONS")
}

func (s *Server) handleIPv6MitigationStatus(w http.ResponseWriter, _ *http.Request) {
	st, err := ipv6mitigation.LoadState(ipv6StatePath())
	if err != nil {
		s.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.respondJSON(w, http.StatusOK, st)
}

func (s *Server) handleIPv6Disable(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Interface string `json:"interface"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSettingsSmallRequestBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		s.respondError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	var extra struct{}
	if err := dec.Decode(&extra); err == nil {
		s.respondError(w, http.StatusBadRequest, "invalid body: multiple JSON values")
		return
	} else if !errors.Is(err, io.EOF) {
		s.respondError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if err := ipv6mitigation.Disable(r.Context(), ipv6StatePath(), body.Interface); err != nil {
		s.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.handleIPv6MitigationStatus(w, r)
}

func (s *Server) handleIPv6Restore(w http.ResponseWriter, r *http.Request) {
	if err := ipv6mitigation.Restore(r.Context(), ipv6StatePath()); err != nil {
		s.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.handleIPv6MitigationStatus(w, r)
}

func (s *Server) handleDNSLeakTest(w http.ResponseWriter, r *http.Request) {
	settings, err := config.LoadAppSettings(config.AppSettingsFile)
	if err != nil {
		settings = config.DefaultAppSettings()
	}
	ctx, cancel := context.WithTimeout(r.Context(), leakTestTimeout)
	defer cancel()
	report, err := leaktest.RunDNSLeakTest(ctx, leaktest.DNSConfig{
		Domain:            settings.LeakTest.Domain,
		ReportURL:         settings.LeakTest.ReportURL,
		ExpectedResolvers: settings.LeakTest.ExpectedResolvers,
	})
	if err != nil {
		s.respondError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.respondJSON(w, http.StatusOK, report)
}

func (s *Server) handleIPv6LeakTest(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), leakTestTimeout)
	defer cancel()
	report, err := leaktest.RunIPv6LeakTest(ctx, nil)
	if err != nil {
		s.respondError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.respondJSON(w, http.StatusOK, report)
}

func (s *Server) handleLeakTestSummary(w http.ResponseWriter, r *http.Request) {
	settings, err := config.LoadAppSettings(config.AppSettingsFile)
	if err != nil {
		settings = config.DefaultAppSettings()
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	dns, dnsErr := leaktest.RunDNSLeakTest(ctx, leaktest.DNSConfig{
		Domain:            settings.LeakTest.Domain,
		ReportURL:         settings.LeakTest.ReportURL,
		ExpectedResolvers: settings.LeakTest.ExpectedResolvers,
	})
	ipv6, ipv6Err := leaktest.RunIPv6LeakTest(ctx, nil)
	s.respondJSON(w, http.StatusOK, map[string]interface{}{
		"dns":        dns,
		"dns_error":  errorString(dnsErr),
		"ipv6":       ipv6,
		"ipv6_error": errorString(ipv6Err),
	})
}

func (s *Server) handleWebRTCTest(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(leaktest.WebRTCTestHTML)); err != nil {
		s.logger.Warn("handleWebRTCTest: write response: %v", err)
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func ipv6StatePath() string {
	return filepath.Join(config.DataDir, "network_state.json")
}
