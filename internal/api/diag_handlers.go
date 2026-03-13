package api

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

const (
	singBoxAPIBase = "http://127.0.0.1:9090"
	testProxyAddr  = "http://127.0.0.1:10807"
)

func SetupDiagRoutes(s *Server) {
	// BUG FIX: sync.Once гарантирует что фоновые горутины запускаются один раз.
	// Без этого повторный вызов SetupDiagRoutes (например в тестах) накапливал
	// дублирующие горутины, каждая из которых поллила sing-box API независимо.
	diagOnce.Do(func() {
		go trafficCollector.run()
		go connTracker.run()
	})
	s.router.HandleFunc("/api/stats", handleStats).Methods("GET", "OPTIONS")
	s.router.HandleFunc("/api/debug/stats", handleDebugStats).Methods("GET", "OPTIONS")
	s.router.HandleFunc("/api/connections", handleConnections).Methods("GET", "OPTIONS")
	s.router.HandleFunc("/api/diagnostics/test", handleDiagTest).Methods("GET", "OPTIONS")
}

// ── Total traffic: streaming /traffic ────────────────────────────────────────
// sing-box отдаёт {"up":N,"down":N} раз в секунду — это уже bytes/s

type trafficSnapshot struct {
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

type trafficStore struct {
	mu      sync.RWMutex
	current trafficSnapshot
	lastOK  time.Time
}

var trafficCollector = &trafficStore{}

var diagOnce sync.Once

func (ts *trafficStore) run() {
	for {
		ts.connect()
		time.Sleep(3 * time.Second)
	}
}

func (ts *trafficStore) connect() {
	client := &http.Client{Timeout: 0}
	resp, err := client.Get(singBoxAPIBase + "/traffic")
	if err != nil {
		return
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var snap trafficSnapshot
		if err := json.Unmarshal(line, &snap); err != nil {
			continue
		}
		ts.mu.Lock()
		ts.current = snap
		ts.lastOK = time.Now()
		ts.mu.Unlock()
	}
}

func (ts *trafficStore) get() (trafficSnapshot, bool) {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	ok := !ts.lastOK.IsZero() && time.Since(ts.lastOK) < 10*time.Second
	return ts.current, ok
}

// ── Per-outbound speed via cumulative delta ───────────────────────────────────
//
// sing-box Clash API возвращает upload/download как накопленные байты.
// uploadSpeed/downloadSpeed могут быть равны 0 если sing-box их не считает.
// Считаем скорость сами через дельту между опросами.
//
// Ключевые нюансы:
//  1. Соединения могут не иметь стабильного ID — используем составной ключ
//     host+outbound+network как fallback.
//  2. Outbound в sing-box Clash API: поле называется "outbound" (проверено).

// connKey struct удалён: connKeyFor() возвращает string, struct остался от старого рефакторинга.
// Оставлять неиспользуемый тип — staticcheck U1000.

type connSample struct {
	upload   int64
	download int64
}

type outboundSpeed struct {
	UpSpeed int64
	DnSpeed int64
}

type connSpeedTracker struct {
	mu       sync.RWMutex
	prev     map[string]connSample // ключ → предыдущий снимок
	speeds   map[string]outboundSpeed
	active   atomic.Int64 // кол-во активных соединений
	lastTick time.Time
}

var connTracker = &connSpeedTracker{
	prev:   make(map[string]connSample),
	speeds: make(map[string]outboundSpeed),
}

func (ct *connSpeedTracker) run() {
	for {
		time.Sleep(2 * time.Second)
		ct.tick()
	}
}

func (ct *connSpeedTracker) tick() {
	conns, err := fetchConnectionsData()
	ct.active.Store(int64(len(conns)))
	if err != nil || len(conns) == 0 {
		ct.mu.Lock()
		ct.speeds = make(map[string]outboundSpeed)
		ct.mu.Unlock()
		return
	}

	now := time.Now()
	ct.mu.Lock()
	defer ct.mu.Unlock()

	dt := now.Sub(ct.lastTick).Seconds()
	if dt < 0.1 || ct.lastTick.IsZero() {
		// Первый тик — только устанавливаем baseline, скорости не считаем
		ct.lastTick = now
		ct.prev = buildSamples(conns)
		return
	}
	ct.lastTick = now

	newSpeeds := make(map[string]outboundSpeed)
	newPrev := buildSamples(conns)

	for _, c := range conns {
		key := connKeyFor(c)
		prev, existed := ct.prev[key]
		if !existed {
			continue
		}
		dUp := c.Upload - prev.upload
		dDn := c.Download - prev.download
		if dUp < 0 { dUp = 0 }
		if dDn < 0 { dDn = 0 }

		ob := c.effectiveOutbound()
		sp := newSpeeds[ob]
		sp.UpSpeed += int64(float64(dUp) / dt)
		sp.DnSpeed += int64(float64(dDn) / dt)
		newSpeeds[ob] = sp
	}

	ct.prev = newPrev
	ct.speeds = newSpeeds
}

func buildSamples(conns []clashConn) map[string]connSample {
	m := make(map[string]connSample, len(conns))
	for _, c := range conns {
		m[connKeyFor(c)] = connSample{upload: c.Upload, download: c.Download}
	}
	return m
}

func connKeyFor(c clashConn) string {
	// Используем ID если он есть (не пустой)
	if c.ID != "" {
		return c.ID
	}
	// Fallback: host + outbound + network (устойчивый для долгих соединений)
	host := c.Metadata.Host
	if host == "" {
		host = c.Metadata.DestinationIP
	}
	return host + "|" + c.effectiveOutbound() + "|" + c.Metadata.Network
}

func (ct *connSpeedTracker) getSpeeds() (proxyUp, proxyDn, dirUp, dirDn int64) {
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	if p, ok := ct.speeds["proxy-out"]; ok {
		proxyUp, proxyDn = p.UpSpeed, p.DnSpeed
	}
	if d, ok := ct.speeds["direct"]; ok {
		dirUp, dirDn = d.UpSpeed, d.DnSpeed
	}
	return
}

// ── Stats ─────────────────────────────────────────────────────────────────────

type StatsResponse struct {
	Up      int64 `json:"up"`
	Down    int64 `json:"down"`
	ProxyUp int64 `json:"proxy_up"`
	ProxyDn int64 `json:"proxy_dn"`
	DirUp   int64 `json:"direct_up"`
	DirDn   int64 `json:"direct_dn"`
	Active  int   `json:"active_connections"`
	OK      bool  `json:"ok"`
}

func handleStats(w http.ResponseWriter, _ *http.Request) {
	snap, ok := trafficCollector.get()
	resp := StatsResponse{
		Up:     snap.Up,
		Down:   snap.Down,
		OK:     ok,
		Active: int(connTracker.active.Load()),
	}
	resp.ProxyUp, resp.ProxyDn, resp.DirUp, resp.DirDn = connTracker.getSpeeds()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// ── Connections struct ────────────────────────────────────────────────────────
// sing-box Clash API (verified field names):
//   - "outbound"     — outbound tag name ("proxy-out", "direct", "block")
//   - "upload"       — cumulative bytes sent
//   - "download"     — cumulative bytes received
//   - "metadata.processPath" — full path to process (camelCase!)
//   - "metadata.host" — destination hostname

type clashConn struct {
	ID       string   `json:"id"`
	Outbound string   `json:"outbound"`
	Chains   []string `json:"chains"` // sing-box: цепочка outbounds (первый = финальный)
	Upload   int64    `json:"upload"`
	Download int64    `json:"download"`
	Metadata struct {
		Host          string `json:"host"`
		DestinationIP string `json:"destinationIP"`
		Network       string `json:"network"`
		ProcessPath   string `json:"processPath"` // camelCase! не process_path
	} `json:"metadata"`
	Rule        string `json:"rule"`
	RulePayload string `json:"rulePayload"`
}

// effectiveOutbound возвращает реальный outbound тег.
// sing-box Clash API иногда не заполняет "outbound" но заполняет "chains".
func (c *clashConn) effectiveOutbound() string {
	if c.Outbound != "" {
		return c.Outbound
	}
	if len(c.Chains) > 0 && c.Chains[0] != "" {
		return c.Chains[0]
	}
	return ""
}

func fetchConnectionsData() ([]clashConn, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	r, err := client.Get(singBoxAPIBase + "/connections")
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	var cr struct {
		Connections []clashConn `json:"connections"`
	}
	if err := json.NewDecoder(r.Body).Decode(&cr); err != nil {
		return nil, err
	}
	return cr.Connections, nil
}

// handleDebugStats — временный debug endpoint для диагностики скорости.
// Возвращает сырые данные из sing-box + текущие вычисленные скорости.
func handleDebugStats(w http.ResponseWriter, _ *http.Request) {
	conns, err := fetchConnectionsData()
	snap, snapOK := trafficCollector.get()
	
	connTracker.mu.RLock()
	speeds := make(map[string]interface{})
	for k, v := range connTracker.speeds {
		speeds[k] = map[string]int64{"up": v.UpSpeed, "dn": v.DnSpeed}
	}
	prevCount := len(connTracker.prev)
	connTracker.mu.RUnlock()

	// Первые 5 соединений для диагностики
	sample := []map[string]interface{}{}
	for i, c := range conns {
		if i >= 5 { break }
		sample = append(sample, map[string]interface{}{
			"id":       c.ID,
			"outbound": c.Outbound,
			"upload":   c.Upload,
			"download": c.Download,
			"host":     c.Metadata.Host,
			"proc":     c.Metadata.ProcessPath,
			"chains":  c.Chains,
			"rule":    c.Rule,
			"network":  c.Metadata.Network,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"traffic_snap":      snap,
		"traffic_snap_ok":   snapOK,
		"speeds_by_outbound": speeds,
		"prev_count":        prevCount,
		"total_conns":       len(conns),
		"err":               func() string { if err != nil { return err.Error() }; return "" }(),
		"sample_conns":      sample,
	})
}

func handleConnections(w http.ResponseWriter, _ *http.Request) {
	conns, err := fetchConnectionsData()
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"connections": []interface{}{}, "error": "sing-box api unavailable",
		})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"connections": conns})
}

// ── Diagnostics ───────────────────────────────────────────────────────────────

type DiagResult struct {
	OK         bool   `json:"ok"`
	LatencyMs  int64  `json:"latency_ms"`
	ExternalIP string `json:"external_ip,omitempty"`
	Error      string `json:"error,omitempty"`
}

func handleDiagTest(w http.ResponseWriter, _ *http.Request) {
	proxyURL, _ := url.Parse(testProxyAddr)
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		Timeout:   10 * time.Second,
	}
	start := time.Now()
	resp, err := client.Get("https://api.ipify.org?format=json")
	elapsed := time.Since(start).Milliseconds()
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		_ = json.NewEncoder(w).Encode(DiagResult{OK: false, LatencyMs: elapsed, Error: err.Error()})
		return
	}
	defer resp.Body.Close()
	var ipResp struct {
		IP string `json:"ip"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&ipResp)
	_ = json.NewEncoder(w).Encode(DiagResult{OK: true, LatencyMs: elapsed, ExternalIP: ipResp.IP})
}
