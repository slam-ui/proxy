package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"proxyclient/internal/clipboard"
	"proxyclient/internal/config"
	"proxyclient/internal/connhistory"
	"proxyclient/internal/crashreport"
	"proxyclient/internal/speedtest"
	"proxyclient/internal/trafficstats"
)

var builtinProfiles = map[string]interface{}{
	"bypass-ru": map[string]interface{}{
		"name": "Только заблокированное",
		"routing": config.RoutingConfig{DefaultAction: config.ActionDirect, Rules: []config.RoutingRule{
			{Value: "geosite:ru-blocked", Type: config.RuleTypeGeosite, Action: config.ActionProxy},
			{Value: "geosite:category-ads-all", Type: config.RuleTypeGeosite, Action: config.ActionBlock},
		}, BlockQUIC: true},
	},
	"proxy-all": map[string]interface{}{
		"name": "Всё через прокси",
		"routing": config.RoutingConfig{DefaultAction: config.ActionProxy, Rules: []config.RoutingRule{
			{Value: "geosite:ru", Type: config.RuleTypeGeosite, Action: config.ActionDirect},
			{Value: "geosite:private", Type: config.RuleTypeGeosite, Action: config.ActionDirect},
		}, BlockQUIC: true},
	},
	"work": map[string]interface{}{
		"name":    "Только рабочие домены",
		"routing": config.RoutingConfig{DefaultAction: config.ActionDirect, Rules: []config.RoutingRule{}, BlockQUIC: true},
	},
}

func SetupImprovementRoutes(s *Server) {
	api := s.router.PathPrefix("/api").Subrouter()
	api.HandleFunc("/speedtest", s.handleSpeedTest).Methods("POST", "OPTIONS")
	api.HandleFunc("/leak-check", s.handleLeakCheck).Methods("GET", "OPTIONS")
	api.HandleFunc("/clipboard/vless", s.handleClipboardVLESS).Methods("GET", "OPTIONS")
	api.HandleFunc("/stats/total", s.handleStatsTotal).Methods("GET", "OPTIONS")
	api.HandleFunc("/diagnostics/crashes", s.handleCrashReports).Methods("GET", "OPTIONS")
	api.HandleFunc("/connections/history", s.handleConnectionHistory).Methods("GET", "OPTIONS")
	api.HandleFunc("/settings/lan-info", s.handleLANInfo).Methods("GET", "OPTIONS")
	api.HandleFunc("/backup/export", s.handleExportConfig).Methods("GET", "OPTIONS")
	api.HandleFunc("/backup/import", s.handleImportConfig).Methods("POST", "OPTIONS")
	api.HandleFunc("/tun/rules/import", s.handleImportRules).Methods("POST", "OPTIONS")
	api.HandleFunc("/profiles/builtins", s.handleBuiltinProfiles).Methods("GET", "OPTIONS")
}

func (s *Server) handleSpeedTest(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	s.respondJSON(w, http.StatusOK, speedtest.Run(ctx, config.ProxyAddr))
}

func (s *Server) handleLeakCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	proxyURL, _ := url.Parse("http://" + config.ProxyAddr)
	proxyClient := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}, Timeout: 8 * time.Second}
	directClient := &http.Client{Timeout: 8 * time.Second}
	proxyIP := fetchIP(ctx, proxyClient)
	directIP := fetchIP(ctx, directClient)
	s.respondJSON(w, http.StatusOK, map[string]interface{}{
		"direct_ip": directIP,
		"proxy_ip":  proxyIP,
		"leaked":    directIP != "" && proxyIP != "" && directIP == proxyIP,
	})
}

func fetchIP(ctx context.Context, client *http.Client) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.ipify.org?format=text", nil)
	if err != nil {
		return ""
	}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 128))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(body))
}

func (s *Server) handleClipboardVLESS(w http.ResponseWriter, _ *http.Request) {
	text := strings.TrimSpace(clipboard.Read())
	if !strings.HasPrefix(text, "vless://") {
		s.respondJSON(w, http.StatusOK, map[string]interface{}{"found": false})
		return
	}
	if _, err := config.ParseVLESSContent(text); err != nil {
		s.respondJSON(w, http.StatusOK, map[string]interface{}{"found": false})
		return
	}
	s.respondJSON(w, http.StatusOK, map[string]interface{}{"found": true, "url": text})
}

func (s *Server) handleStatsTotal(w http.ResponseWriter, _ *http.Request) {
	s.respondJSON(w, http.StatusOK, trafficstats.Current())
}

func (s *Server) handleCrashReports(w http.ResponseWriter, _ *http.Request) {
	s.respondJSON(w, http.StatusOK, map[string]interface{}{"reports": crashreport.ListLatest(5)})
}

func (s *Server) handleConnectionHistory(w http.ResponseWriter, _ *http.Request) {
	s.respondJSON(w, http.StatusOK, map[string]interface{}{"events": connhistory.Global.All()})
}

func (s *Server) handleLANInfo(w http.ResponseWriter, _ *http.Request) {
	port := 10808
	if cfg, err := config.LoadRoutingConfig(filepath.Join(config.DataDir, "routing.json")); err == nil {
		config.SanitizeRoutingConfig(cfg)
		port = cfg.LANSharePort
	}
	var ips []string
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip4 := ip.To4(); ip4 != nil && !ip4.IsLoopback() {
				ips = append(ips, ip4.String())
			}
		}
	}
	s.respondJSON(w, http.StatusOK, map[string]interface{}{"ips": ips, "port": port})
}

func (s *Server) handleBuiltinProfiles(w http.ResponseWriter, _ *http.Request) {
	s.respondJSON(w, http.StatusOK, map[string]interface{}{"profiles": builtinProfiles})
}

func (s *Server) handleExportConfig(w http.ResponseWriter, r *http.Request) {
	s.handleBackup(w, r)
}

func (s *Server) handleImportConfig(w http.ResponseWriter, r *http.Request) {
	s.handleBackupRestore(w, r)
}

func (s *Server) handleImportRules(w http.ResponseWriter, r *http.Request) {
	if s.tunHandlers == nil {
		s.respondError(w, http.StatusServiceUnavailable, "tun handlers not initialized")
		return
	}
	var req struct {
		Format  string            `json:"format"`
		Content string            `json:"content"`
		Action  config.RuleAction `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.respondError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	rules, err := parseImportedRules(req.Format, req.Content, req.Action)
	if err != nil {
		s.respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.tunHandlers.mu.Lock()
	s.tunHandlers.routing.Rules = append(s.tunHandlers.routing.Rules, rules...)
	snapshot := cloneRoutingConfig(s.tunHandlers.routing)
	s.tunHandlers.mu.Unlock()
	if err := config.SaveRoutingConfig(config.DataDir+"/routing.json", snapshot); err != nil {
		s.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.tunHandlers.TriggerApply(); err != nil {
		s.logger.Warn("handleImportRules: TriggerApply: %v", err)
	}
	s.respondJSON(w, http.StatusOK, map[string]interface{}{"imported": len(rules)})
}

func parseImportedRules(format, content string, action config.RuleAction) ([]config.RoutingRule, error) {
	if action == "" {
		action = config.ActionProxy
	}
	var lines []string
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "text":
		lines = strings.Split(content, "\n")
	case "clash":
		for _, line := range strings.Split(content, "\n") {
			parts := strings.Split(line, ",")
			if len(parts) >= 2 {
				lines = append(lines, strings.TrimSpace(parts[1]))
			}
		}
	case "gfwlist":
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(content))
		if err != nil {
			return nil, err
		}
		lines = strings.Split(string(decoded), "\n")
	default:
		return nil, fmt.Errorf("unsupported format")
	}
	rules := make([]config.RoutingRule, 0, len(lines))
	seen := map[string]bool{}
	for _, line := range lines {
		val := config.NormalizeRuleValue(strings.TrimSpace(strings.TrimPrefix(line, "||")))
		val = strings.TrimPrefix(val, ".")
		if val == "" || strings.HasPrefix(val, "!") || strings.HasPrefix(val, "[") || seen[val] {
			continue
		}
		seen[val] = true
		rules = append(rules, config.RoutingRule{Value: val, Type: config.DetectRuleType(val), Action: action})
	}
	return rules, nil
}

func parseClockHM(s string) (int, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, fmt.Errorf("bad time")
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, err
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, err
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, fmt.Errorf("bad time")
	}
	return h*60 + m, nil
}

func IsWithinSchedule(now time.Time, schedule config.Schedule) bool {
	if !schedule.Enabled {
		return false
	}
	if len(schedule.Weekdays) > 0 {
		day := int(now.Weekday())
		if day == 0 {
			day = 7
		}
		ok := false
		for _, wd := range schedule.Weekdays {
			if wd == day {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	on, err := parseClockHM(schedule.ProxyOn)
	if err != nil {
		return false
	}
	off, err := parseClockHM(schedule.ProxyOff)
	if err != nil {
		return false
	}
	minute := now.Hour()*60 + now.Minute()
	if on <= off {
		return minute >= on && minute < off
	}
	return minute >= on || minute < off
}
