package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"proxyclient/internal/config"
	"proxyclient/internal/logger"
	"proxyclient/internal/proxy"
)

const clashModeTimeout = 2 * time.Second

var clashModeHTTPClient = &http.Client{Timeout: clashModeTimeout}

type clashModeRequest struct {
	Mode string `json:"mode"`
}

func defaultProxyConfig() proxy.Config {
	return proxy.Config{
		Address:  DefaultProxyAddress,
		Override: DefaultProxyOverride,
	}
}

func (s *Server) markProxyEnabledAt(t time.Time) {
	s.proxyEnabledAtMu.Lock()
	s.proxyEnabledAt = t
	s.proxyEnabledAtMu.Unlock()
}

func (s *Server) clearProxyEnabledAt() {
	s.proxyEnabledAtMu.Lock()
	s.proxyEnabledAt = time.Time{}
	s.proxyEnabledAtMu.Unlock()
}

func (s *Server) enableSystemProxy() error {
	if err := s.config.ProxyManager.Enable(defaultProxyConfig()); err != nil {
		return err
	}
	s.markProxyEnabledAt(time.Now())
	return nil
}

func (s *Server) disableSystemProxy() error {
	if err := s.config.ProxyManager.Disable(); err != nil {
		return err
	}
	s.clearProxyEnabledAt()
	return nil
}

func (s *Server) handleProxyEnable(w http.ResponseWriter, r *http.Request) {
	s.proxyOpMu.Lock()
	defer s.proxyOpMu.Unlock()

	if s.config.ProxyManager.IsEnabled() {
		s.respondError(w, http.StatusBadRequest, "прокси уже включен")
		return
	}
	if err := s.enableSystemProxy(); err != nil {
		s.logger.Error("Не удалось включить прокси: %v", err)
		s.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	switchClashMode(r.Context(), s.logger, "rule")
	s.respondJSON(w, http.StatusOK, MessageResponse{Message: "Прокси успешно включен", Success: true})
}

func (s *Server) handleProxyDisable(w http.ResponseWriter, r *http.Request) {
	s.proxyOpMu.Lock()
	defer s.proxyOpMu.Unlock()

	if !s.config.ProxyManager.IsEnabled() {
		s.respondError(w, http.StatusBadRequest, "прокси уже отключен")
		return
	}
	if err := s.disableSystemProxy(); err != nil {
		s.logger.Error("Не удалось отключить прокси: %v", err)
		s.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	switchClashMode(r.Context(), s.logger, "direct")
	s.respondJSON(w, http.StatusOK, MessageResponse{Message: "Прокси успешно отключен", Success: true})
}

func (s *Server) handleProxyToggle(w http.ResponseWriter, r *http.Request) {
	s.proxyOpMu.Lock()
	defer s.proxyOpMu.Unlock()

	if s.config.ProxyManager.IsEnabled() {
		if err := s.disableSystemProxy(); err != nil {
			s.respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		switchClashMode(r.Context(), s.logger, "direct")
		s.respondJSON(w, http.StatusOK, MessageResponse{Message: "Прокси отключен", Success: true})
		return
	}
	if err := s.enableSystemProxy(); err != nil {
		s.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	switchClashMode(r.Context(), s.logger, "rule")
	s.respondJSON(w, http.StatusOK, MessageResponse{Message: "Прокси включен", Success: true})
}

// switchClashMode переключает режим Clash API (rule/direct).
// Использует таймаут 2s чтобы не блокировать хендлер при недоступном Clash API.
func switchClashMode(ctx context.Context, log logger.Logger, mode string) {
	body, err := json.Marshal(clashModeRequest{Mode: mode})
	if err != nil {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, clashAPIURL+"/configs", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+config.ClashAPISecret())
	resp, err := clashModeHTTPClient.Do(req)
	if err != nil {
		if log != nil {
			log.Debug("Clash API недоступен при смене режима на %q: %v", mode, err)
		}
		return
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		if log != nil {
			log.Debug("Clash API вернул %d при смене режима на %q", resp.StatusCode, mode)
		}
		return
	}
	if log != nil {
		log.Info("TUN режим переключён: %s", mode)
	}
}
