package api

import (
	"net/http"

	"proxyclient/internal/config"
)

type securityStatusResponse struct {
	Tunnel       securityTunnelStatus `json:"tunnel"`
	DNSGuard     securityDNSStatus    `json:"dns_guard"`
	BackupServer securityBackupStatus `json:"backup_server"`
}

type securityTunnelStatus struct {
	Active bool `json:"active"`
}

type securityDNSStatus struct {
	Enabled bool   `json:"enabled"`
	Mode    string `json:"mode"`
}

type securityBackupStatus struct {
	Available bool `json:"available"`
	Count     int  `json:"count"`
}

func (s *Server) handleSecurityStatus(w http.ResponseWriter, _ *http.Request) {
	settings, err := config.LoadAppSettings(config.AppSettingsFile)
	if err != nil && s.logger != nil {
		s.logger.Warn("handleSecurityStatus: LoadAppSettings: %v", err)
		settings = config.DefaultAppSettings()
	}

	serverCount := s.visibleServerCount()
	s.respondJSON(w, http.StatusOK, securityStatusResponse{
		Tunnel: securityTunnelStatus{
			Active: s.tunnelActive(),
		},
		DNSGuard: securityDNSStatus{
			Enabled: settings.DNSGuard.Enabled,
			Mode:    settings.DNSGuard.Mode,
		},
		BackupServer: securityBackupStatus{
			Available: serverCount > 1,
			Count:     serverCount,
		},
	})
}

func (s *Server) tunnelActive() bool {
	s.configMu.RLock()
	xrayMgr := s.config.XRayManager
	proxyMgr := s.config.ProxyManager
	s.configMu.RUnlock()

	if xrayMgr != nil && xrayMgr.IsRunning() {
		return true
	}
	return proxyMgr != nil && proxyMgr.IsEnabled()
}

func (s *Server) visibleServerCount() int {
	if s.serversHandlers == nil {
		return 0
	}
	s.serversHandlers.mu.RLock()
	defer s.serversHandlers.mu.RUnlock()

	list, err := loadServers()
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("handleSecurityStatus: loadServers: %v", err)
		}
		return 0
	}
	return len(visibleServers(list))
}
