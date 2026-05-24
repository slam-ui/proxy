package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"proxyclient/internal/config"
	"proxyclient/internal/connhistory"
	"proxyclient/internal/netwatch"
	"proxyclient/internal/trafficstats"

	"github.com/gorilla/mux"
)

const maxClientFeaturesRequestBytes = 4 << 10

const temporaryRuleNotePrefix = "expires:"

type clientRuleFinding struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	Index    int    `json:"index,omitempty"`
	Value    string `json:"value,omitempty"`
	Fixable  bool   `json:"fixable,omitempty"`
}

type inspectedConnection struct {
	ID          string `json:"id"`
	Process     string `json:"process"`
	ProcessPath string `json:"process_path,omitempty"`
	Target      string `json:"target"`
	Network     string `json:"network,omitempty"`
	Outbound    string `json:"outbound,omitempty"`
	Action      string `json:"action"`
	Rule        string `json:"rule,omitempty"`
	RulePayload string `json:"rule_payload,omitempty"`
	Upload      int64  `json:"upload"`
	Download    int64  `json:"download"`
}

func SetupClientFeatureRoutes(s *Server, ctx context.Context) {
	api := s.router.PathPrefix("/api").Subrouter()
	api.HandleFunc("/tun/rules/analyze", s.handleRuleAnalyze).Methods("GET", "OPTIONS")
	api.HandleFunc("/tun/rules/analyze/fix", s.handleRuleAnalyzeFix).Methods("POST", "OPTIONS")
	api.HandleFunc("/connections/inspect", s.handleConnectionsInspect).Methods("GET", "OPTIONS")
	api.HandleFunc("/connections/rule", s.handleConnectionRule).Methods("POST", "OPTIONS")
	api.HandleFunc("/temporary-rules", s.handleTemporaryRulesList).Methods("GET", "OPTIONS")
	api.HandleFunc("/temporary-rules", s.handleTemporaryRuleAdd).Methods("POST", "OPTIONS")
	api.HandleFunc("/temporary-rules/{value:.+}", s.handleTemporaryRuleDelete).Methods("DELETE", "OPTIONS")
	api.HandleFunc("/security/dns-guard", s.handleDNSGuardGet).Methods("GET", "OPTIONS")
	api.HandleFunc("/security/dns-guard", s.handleDNSGuardSet).Methods("POST", "OPTIONS")
	api.HandleFunc("/security/dns-guard/check", s.handleDNSGuardCheck).Methods("GET", "OPTIONS")
	api.HandleFunc("/security/network", s.handleNetworkProtectionGet).Methods("GET", "OPTIONS")
	api.HandleFunc("/security/traffic-budget", s.handleTrafficBudgetGet).Methods("GET", "OPTIONS")
	api.HandleFunc("/security/traffic-budget", s.handleTrafficBudgetSet).Methods("POST", "OPTIONS")
	api.HandleFunc("/diagnostics/integrity", s.handleIntegrityCheck).Methods("GET", "OPTIONS")
	api.HandleFunc("/diagnostics/package", s.handleDiagnosticsPackage).Methods("GET", "OPTIONS")

	s.startNetworkProtection(ctx)
	s.startTemporaryRuleExpiry(ctx)
}

func (s *Server) currentRoutingSnapshot() *config.RoutingConfig {
	if s.tunHandlers != nil {
		s.tunHandlers.mu.RLock()
		defer s.tunHandlers.mu.RUnlock()
		return cloneRoutingConfig(s.tunHandlers.routing)
	}
	routing, err := config.LoadRoutingConfig(routingConfigPath)
	if err != nil {
		return config.DefaultRoutingConfig()
	}
	return routing
}

func (s *Server) replaceRoutingSnapshot(next *config.RoutingConfig) error {
	config.SanitizeRoutingConfig(next)
	if err := config.SaveRoutingConfig(routingConfigPath, next); err != nil {
		return err
	}
	if s.tunHandlers != nil {
		s.tunHandlers.mu.Lock()
		s.tunHandlers.routing = cloneRoutingConfig(next)
		s.tunHandlers.mu.Unlock()
		if err := s.tunHandlers.TriggerApply(); err != nil {
			s.logger.Warn("replaceRoutingSnapshot: TriggerApply: %v", err)
		}
	}
	return nil
}

func (s *Server) mutateRoutingSnapshot(mutator func(*config.RoutingConfig) (bool, error)) error {
	s.routingOpMu.Lock()
	defer s.routingOpMu.Unlock()

	routing := s.currentRoutingSnapshot()
	changed, err := mutator(routing)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	return s.replaceRoutingSnapshot(routing)
}

func (s *Server) handleRuleAnalyze(w http.ResponseWriter, _ *http.Request) {
	routing := s.currentRoutingSnapshot()
	findings := analyzeRoutingRules(routing)
	s.respondJSON(w, http.StatusOK, map[string]interface{}{
		"ok":       len(findings) == 0,
		"count":    len(findings),
		"findings": findings,
	})
}

func (s *Server) handleRuleAnalyzeFix(w http.ResponseWriter, _ *http.Request) {
	var before, after int
	if err := s.mutateRoutingSnapshot(func(routing *config.RoutingConfig) (bool, error) {
		before = len(routing.Rules)
		routing.Rules = fixRoutingRules(routing.Rules)
		smartSortRoutingRules(routing.Rules)
		after = len(routing.Rules)
		return true, nil
	}); err != nil {
		s.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"before":  before,
		"after":   after,
	})
}

func analyzeRoutingRules(routing *config.RoutingConfig) []clientRuleFinding {
	if routing == nil {
		return nil
	}
	var findings []clientRuleFinding
	seen := map[string]int{}
	processBeforeDirect := false
	for i, rule := range routing.Rules {
		value := config.NormalizeRuleValue(rule.Value)
		key := string(rule.Type) + "|" + string(rule.Action) + "|" + value
		if first, ok := seen[key]; ok {
			findings = append(findings, clientRuleFinding{
				Severity: "warn", Code: "duplicate", Index: i, Value: rule.Value, Fixable: true,
				Message: fmt.Sprintf("дубликат правила #%d", first+1),
			})
		} else {
			seen[key] = i
		}
		if value == "" {
			findings = append(findings, clientRuleFinding{Severity: "error", Code: "empty", Index: i, Fixable: true, Message: "пустое правило"})
		}
		if rule.Action != config.ActionProxy && rule.Action != config.ActionDirect && rule.Action != config.ActionBlock {
			findings = append(findings, clientRuleFinding{Severity: "error", Code: "bad_action", Index: i, Value: string(rule.Action), Message: "неизвестное действие"})
		}
		if rule.Type == config.RuleTypeIP && net.ParseIP(value) == nil {
			if _, _, err := net.ParseCIDR(value); err != nil {
				findings = append(findings, clientRuleFinding{Severity: "error", Code: "bad_cidr", Index: i, Value: rule.Value, Message: "IP/CIDR не распознан"})
			}
		}
		if rule.Type == config.RuleTypeGeosite {
			name := strings.TrimPrefix(value, "geosite:")
			if name == "" {
				findings = append(findings, clientRuleFinding{Severity: "error", Code: "bad_geosite", Index: i, Value: rule.Value, Message: "пустой geosite"})
			} else if _, err := os.Stat(filepath.Join(config.DataDir, "geosite-"+name+".bin")); os.IsNotExist(err) {
				findings = append(findings, clientRuleFinding{Severity: "warn", Code: "missing_geosite", Index: i, Value: rule.Value, Message: "geosite база не скачана"})
			}
		}
		if rule.Type == config.RuleTypeProcess {
			processBeforeDirect = true
		}
		if processBeforeDirect && rule.Type == config.RuleTypeDomain && rule.Action == config.ActionDirect {
			findings = append(findings, clientRuleFinding{
				Severity: "warn", Code: "shadowed_direct", Index: i, Value: rule.Value, Fixable: true,
				Message: "domain direct ниже process-правила может не сработать",
			})
		}
		if exp, ok := temporaryRuleExpiry(rule); ok && time.Now().After(exp) {
			findings = append(findings, clientRuleFinding{Severity: "warn", Code: "expired_temporary", Index: i, Value: rule.Value, Fixable: true, Message: "временное правило истекло"})
		}
	}
	return findings
}

func fixRoutingRules(rules []config.RoutingRule) []config.RoutingRule {
	out := make([]config.RoutingRule, 0, len(rules))
	seen := map[string]bool{}
	for _, rule := range rules {
		value := config.NormalizeRuleValue(rule.Value)
		if value == "" {
			continue
		}
		if exp, ok := temporaryRuleExpiry(rule); ok && time.Now().After(exp) {
			continue
		}
		rule.Value = value
		if rule.Type == "" {
			rule.Type = config.DetectRuleType(value)
		}
		key := string(rule.Type) + "|" + string(rule.Action) + "|" + rule.Value
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, rule)
	}
	return out
}

func fetchClashConnections(ctx context.Context) ([]clashConn, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clashAPIBaseURL+"/connections", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+config.ClashAPISecret())
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		Connections []clashConn `json:"connections"`
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&body); err != nil {
		return nil, err
	}
	return body.Connections, nil
}

func (s *Server) handleConnectionsInspect(w http.ResponseWriter, r *http.Request) {
	conns, err := fetchClashConnections(r.Context())
	if err != nil {
		s.respondJSON(w, http.StatusOK, map[string]interface{}{"connections": []inspectedConnection{}, "error": err.Error()})
		return
	}
	out := make([]inspectedConnection, 0, len(conns))
	for _, c := range conns {
		target := c.Metadata.Host
		if target == "" {
			target = c.Metadata.DestinationIP
		}
		outbound := c.effectiveOutbound()
		out = append(out, inspectedConnection{
			ID:          c.ID,
			Process:     filepath.Base(c.Metadata.ProcessPath),
			ProcessPath: c.Metadata.ProcessPath,
			Target:      target,
			Network:     c.Metadata.Network,
			Outbound:    outbound,
			Action:      outboundAction(outbound),
			Rule:        c.Rule,
			RulePayload: c.RulePayload,
			Upload:      c.Upload,
			Download:    c.Download,
		})
	}
	s.respondJSON(w, http.StatusOK, map[string]interface{}{"connections": out})
}

func outboundAction(outbound string) string {
	lower := strings.ToLower(outbound)
	switch {
	case strings.Contains(lower, "block"), strings.Contains(lower, "reject"):
		return string(config.ActionBlock)
	case strings.Contains(lower, "direct"):
		return string(config.ActionDirect)
	case strings.Contains(lower, "proxy"), strings.Contains(lower, "select"):
		return string(config.ActionProxy)
	default:
		return "unknown"
	}
}

func (s *Server) handleConnectionRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Value  string            `json:"value"`
		Type   config.RuleType   `json:"type"`
		Action config.RuleAction `json:"action"`
		TTLMin int               `json:"ttl_min"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxClientFeaturesRequestBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		s.respondError(w, http.StatusBadRequest, "invalid body")
		return
	}
	var extra struct{}
	if err := dec.Decode(&extra); err == nil {
		s.respondError(w, http.StatusBadRequest, "invalid body")
		return
	} else if !errors.Is(err, io.EOF) {
		s.respondError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Action == "" {
		req.Action = config.ActionProxy
	}
	value := config.NormalizeRuleValue(req.Value)
	if value == "" || !isValidAction(req.Action) {
		s.respondError(w, http.StatusBadRequest, "invalid rule")
		return
	}
	if req.Type == "" {
		req.Type = config.DetectRuleType(value)
	}
	rule := config.RoutingRule{Value: value, Type: req.Type, Action: req.Action}
	if req.TTLMin > 0 {
		rule.Note = temporaryRuleNotePrefix + fmt.Sprint(time.Now().Add(time.Duration(req.TTLMin)*time.Minute).Unix())
	}
	if err := s.addRoutingRule(rule); err != nil {
		s.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.respondJSON(w, http.StatusOK, map[string]interface{}{"success": true, "rule": rule})
}

func (s *Server) addRoutingRule(rule config.RoutingRule) error {
	return s.mutateRoutingSnapshot(func(routing *config.RoutingConfig) (bool, error) {
		for _, existing := range routing.Rules {
			if existing.Value == rule.Value && existing.Type == rule.Type && existing.Action == rule.Action {
				return false, nil
			}
		}
		routing.Rules = append(routing.Rules, rule)
		return true, nil
	})
}

func (s *Server) handleTemporaryRulesList(w http.ResponseWriter, _ *http.Request) {
	routing := s.currentRoutingSnapshot()
	type item struct {
		config.RoutingRule
		ExpiresAt int64 `json:"expires_at"`
	}
	var items []item
	for _, rule := range routing.Rules {
		if exp, ok := temporaryRuleExpiry(rule); ok {
			items = append(items, item{RoutingRule: rule, ExpiresAt: exp.Unix()})
		}
	}
	s.respondJSON(w, http.StatusOK, map[string]interface{}{"rules": items})
}

func (s *Server) handleTemporaryRuleAdd(w http.ResponseWriter, r *http.Request) {
	s.handleConnectionRule(w, r)
}

func (s *Server) handleTemporaryRuleDelete(w http.ResponseWriter, r *http.Request) {
	value := config.NormalizeRuleValue(mux.Vars(r)["value"])
	removed := 0
	if err := s.mutateRoutingSnapshot(func(routing *config.RoutingConfig) (bool, error) {
		next := routing.Rules[:0]
		for _, rule := range routing.Rules {
			_, temporary := temporaryRuleExpiry(rule)
			if temporary && rule.Value == value {
				removed++
				continue
			}
			next = append(next, rule)
		}
		routing.Rules = next
		return removed > 0, nil
	}); err != nil {
		s.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.respondJSON(w, http.StatusOK, map[string]interface{}{"removed": removed})
}

func temporaryRuleExpiry(rule config.RoutingRule) (time.Time, bool) {
	if !strings.HasPrefix(rule.Note, temporaryRuleNotePrefix) {
		return time.Time{}, false
	}
	unix, err := parseInt64(strings.TrimPrefix(rule.Note, temporaryRuleNotePrefix))
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(unix, 0), true
}

func parseInt64(s string) (int64, error) {
	var n int64
	_, err := fmt.Sscan(strings.TrimSpace(s), &n)
	return n, err
}

func (s *Server) startTemporaryRuleExpiry(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.mutateRoutingSnapshot(func(routing *config.RoutingConfig) (bool, error) {
					next := fixRoutingRules(routing.Rules)
					if len(next) != len(routing.Rules) {
						routing.Rules = next
						return true, nil
					}
					return false, nil
				}); err != nil {
					s.logger.Warn("temporary rules cleanup: %v", err)
				}
			}
		}
	}()
}

func (s *Server) handleDNSGuardGet(w http.ResponseWriter, _ *http.Request) {
	settings, _ := config.LoadAppSettings(config.AppSettingsFile)
	s.respondJSON(w, http.StatusOK, settings.DNSGuard)
}

func (s *Server) handleDNSGuardSet(w http.ResponseWriter, r *http.Request) {
	settings, _ := config.LoadAppSettings(config.AppSettingsFile)
	var body config.DNSGuardSettings
	r.Body = http.MaxBytesReader(w, r.Body, maxClientFeaturesRequestBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		s.respondError(w, http.StatusBadRequest, "invalid body")
		return
	}
	var extra struct{}
	if err := dec.Decode(&extra); err == nil {
		s.respondError(w, http.StatusBadRequest, "invalid body")
		return
	} else if !errors.Is(err, io.EOF) {
		s.respondError(w, http.StatusBadRequest, "invalid body")
		return
	}
	settings.DNSGuard = body
	if err := config.SaveAppSettings(config.AppSettingsFile, settings); err != nil {
		s.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.respondJSON(w, http.StatusOK, settings.DNSGuard)
}

func (s *Server) handleDNSGuardCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	proxyReqURL, _ := url.Parse("http://" + config.ProxyAddr)
	proxyClient := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyReqURL)}, Timeout: 8 * time.Second}
	directClient := &http.Client{Timeout: 8 * time.Second}
	proxyIP := fetchIP(ctx, proxyClient)
	directIP := fetchIP(ctx, directClient)
	leaked := directIP != "" && proxyIP != "" && directIP == proxyIP
	settings, _ := config.LoadAppSettings(config.AppSettingsFile)
	s.respondJSON(w, http.StatusOK, map[string]interface{}{
		"proxy_ip":           proxyIP,
		"direct_ip":          directIP,
		"leaked":             leaked,
		"strict":             settings.DNSGuard.Enabled && settings.DNSGuard.Mode == "strict",
	})
}

func (s *Server) handleNetworkProtectionGet(w http.ResponseWriter, _ *http.Request) {
	settings, _ := config.LoadAppSettings(config.AppSettingsFile)
	s.respondJSON(w, http.StatusOK, map[string]interface{}{
		"settings":    settings.NetworkProtection,
		"fingerprint": networkFingerprint(),
		"interfaces":  networkInterfaces(),
	})
}

func (s *Server) startNetworkProtection(ctx context.Context) {
	go func() {
		tracker := newNetworkChangeTracker(networkFingerprint)
		_ = netwatch.Watch(ctx, func() {
			settings, _ := config.LoadAppSettings(config.AppSettingsFile)
			if !settings.NetworkProtection.Enabled {
				return
			}
			if !tracker.changed() {
				return
			}
			connhistory.Global.Add(connhistory.Event{Time: time.Now(), Kind: connhistory.EventNetChange, Reason: "network fingerprint changed"})
			s.logger.Info("Network protection: сетевой интерфейс изменился")
			if s.serversHandlers != nil && settings.SmartFailover.Enabled {
				go s.serversHandlers.AutoConnect()
			}
		})
	}()
}

type networkChangeTracker struct {
	mu          sync.Mutex
	last        string
	fingerprint func() string
}

func newNetworkChangeTracker(fingerprint func() string) *networkChangeTracker {
	return &networkChangeTracker{
		last:        fingerprint(),
		fingerprint: fingerprint,
	}
}

func (t *networkChangeTracker) changed() bool {
	cur := t.fingerprint()
	t.mu.Lock()
	defer t.mu.Unlock()
	if cur == "" || cur == t.last {
		return false
	}
	t.last = cur
	return true
}

func networkFingerprint() string {
	ifaces := networkInterfaces()
	var parts []string
	for _, item := range ifaces {
		if name, _ := item["name"].(string); name != "" {
			ips, _ := item["ips"].([]string)
			parts = append(parts, name+"="+strings.Join(ips, ","))
		}
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:8])
}

func networkInterfaces() []map[string]interface{} {
	ifaces, _ := net.Interfaces()
	var out []map[string]interface{}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		var ips []string
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			if ipNet, ok := addr.(*net.IPNet); ok && ipNet.IP.To4() != nil {
				ips = append(ips, ipNet.IP.String())
			}
		}
		if len(ips) > 0 {
			out = append(out, map[string]interface{}{"name": iface.Name, "ips": ips})
		}
	}
	return out
}

func (s *Server) handleTrafficBudgetGet(w http.ResponseWriter, _ *http.Request) {
	settings, _ := config.LoadAppSettings(config.AppSettingsFile)
	stats := trafficstats.Current()
	sessionBytes := stats.SessionDownloadBytes + stats.SessionUploadBytes
	totalBytes := stats.TotalDownloadBytes + stats.TotalUploadBytes + sessionBytes
	status := map[string]interface{}{
		"settings":      settings.TrafficBudget,
		"session_bytes": sessionBytes,
		"total_bytes":   totalBytes,
		"session_pct":   budgetPct(sessionBytes, settings.TrafficBudget.SessionLimitMB),
		"total_pct":     budgetPct(totalBytes, settings.TrafficBudget.TotalLimitMB),
	}
	s.respondJSON(w, http.StatusOK, status)
}

func (s *Server) handleTrafficBudgetSet(w http.ResponseWriter, r *http.Request) {
	settings, _ := config.LoadAppSettings(config.AppSettingsFile)
	var body config.TrafficBudgetSettings
	r.Body = http.MaxBytesReader(w, r.Body, maxClientFeaturesRequestBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		s.respondError(w, http.StatusBadRequest, "invalid body")
		return
	}
	var extra struct{}
	if err := dec.Decode(&extra); err == nil {
		s.respondError(w, http.StatusBadRequest, "invalid body")
		return
	} else if !errors.Is(err, io.EOF) {
		s.respondError(w, http.StatusBadRequest, "invalid body")
		return
	}
	settings.TrafficBudget = body
	if err := config.SaveAppSettings(config.AppSettingsFile, settings); err != nil {
		s.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.respondJSON(w, http.StatusOK, settings.TrafficBudget)
}

func budgetPct(bytes int64, limitMB int64) int {
	if limitMB <= 0 || bytes <= 0 {
		return 0
	}
	limitBytes := budgetLimitBytes(limitMB)
	whole := (bytes / limitBytes) * 100
	fraction := ((bytes % limitBytes) * 100) / limitBytes
	return int(whole + fraction)
}

func budgetExceeded(bytes int64, limitMB int64) bool {
	return limitMB > 0 && bytes >= budgetLimitBytes(limitMB)
}

func budgetLimitBytes(limitMB int64) int64 {
	const bytesPerMB int64 = 1024 * 1024
	const maxInt64 = int64(1<<63 - 1)
	if limitMB > maxInt64/bytesPerMB {
		return maxInt64
	}
	return limitMB * bytesPerMB
}

func (s *Server) handleIntegrityCheck(w http.ResponseWriter, _ *http.Request) {
	paths := []string{"sing-box.exe", "wintun.dll", "config.singbox.json", routingConfigPath, s.config.SecretKeyPath}
	var files []map[string]interface{}
	for _, path := range paths {
		if path == "" {
			continue
		}
		sum, size, err := sha256File(path)
		item := map[string]interface{}{"path": path, "ok": err == nil, "sha256": sum, "size": size}
		if err != nil {
			item["error"] = err.Error()
		}
		files = append(files, item)
	}
	s.respondJSON(w, http.StatusOK, map[string]interface{}{"files": files})
}

func sha256File(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", n, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func (s *Server) handleDiagnosticsPackage(w http.ResponseWriter, _ *http.Request) {
	settings, _ := config.LoadAppSettings(config.AppSettingsFile)
	payload := map[string]interface{}{
		"generated_at": time.Now(),
		"status":       s.statusSnapshot(),
		"settings": map[string]interface{}{
			"smart_failover":     settings.SmartFailover,
			"dns_guard":          settings.DNSGuard,
			"network_protection": settings.NetworkProtection,
			"traffic_budget":     settings.TrafficBudget,
		},
		"network": networkInterfaces(),
		"routing": s.currentRoutingSnapshot(),
		"events":  connhistory.Global.All(),
	}
	w.Header().Set("Content-Disposition", `attachment; filename="safesky-diagnostics.json"`)
	s.respondJSON(w, http.StatusOK, payload)
}

func (s *Server) statusSnapshot() map[string]interface{} {
	s.configMu.RLock()
	xrayMgr := s.config.XRayManager
	s.configMu.RUnlock()
	proxyConfig := s.config.ProxyManager.GetConfig()
	snap := map[string]interface{}{
		"proxy_enabled": s.config.ProxyManager.IsEnabled(),
		"proxy_address": proxyConfig.Address,
		"config_path":   s.config.ConfigPath,
		"restarting":    s.IsRestarting(),
		"warming":       s.IsWarming(),
	}
	if xrayMgr != nil {
		errCount, errRate, wouldAlert := xrayMgr.GetHealthStatus()
		snap["xray_running"] = xrayMgr.IsRunning()
		snap["xray_pid"] = xrayMgr.GetPID()
		snap["xray_error_count"] = errCount
		snap["xray_error_rate_pct"] = errRate
		snap["xray_would_alert"] = wouldAlert
	}
	return snap
}
