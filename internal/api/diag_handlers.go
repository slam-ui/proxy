package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"proxyclient/internal/config"
)

// DiagHandlers управляет сбором статистики трафика и соединений.
// В отличие от предыдущей версии с глобальными переменными — имеет явный lifecycle:
// Start(ctx) запускает фоновые горутины, они останавливаются при отмене ctx.
// Это делает код тестируемым: каждый тест создаёт свой экземпляр.
type DiagHandlers struct {
	traffic *trafficStore
	conns   *connSpeedTracker
}

// newDiagHandlers создаёт новый экземпляр DiagHandlers.
func newDiagHandlers() *DiagHandlers {
	return &DiagHandlers{
		traffic: &trafficStore{},
		conns: &connSpeedTracker{
			prev:   make(map[string]connSample),
			speeds: make(map[string]outboundSpeed),
		},
	}
}

// start запускает фоновые горутины сбора данных.
// Они живут пока ctx не отменён — при завершении приложения ctx отменяется и горутины останавливаются.
func (h *DiagHandlers) start(ctx context.Context) {
	go h.traffic.run(ctx)
	go h.conns.run(ctx)
}

// SetupDiagRoutes регистрирует маршруты диагностики и запускает сборщики данных.
func SetupDiagRoutes(s *Server, ctx context.Context) {
	h := newDiagHandlers()
	h.start(ctx)
	s.router.HandleFunc("/api/stats", h.handleStats).Methods("GET", "OPTIONS")
	s.router.HandleFunc("/api/debug/stats", h.handleDebugStats).Methods("GET", "OPTIONS")
	s.router.HandleFunc("/api/connections", h.handleConnections).Methods("GET", "OPTIONS")
	s.router.HandleFunc("/api/diagnostics/test", handleDiagTest).Methods("GET", "OPTIONS")
}

// ── Total traffic: streaming /traffic ────────────────────────────────────────

type trafficSnapshot struct {
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

type trafficStore struct {
	// OPT #5: клиент создаётся один раз — переиспользует пул TCP-соединений к localhost.
	// Ранее новый http.Client на каждый connect() = потеря keep-alive каждые 3с при разрыве.
	client        http.Client
	currentAtomic atomic.Value
	lastOK        atomic.Int64
	mu            sync.Mutex
	sessionUpB    int64
	sessionDnB    int64
	sessionStart  time.Time
}

func (ts *trafficStore) run(ctx context.Context) {
	ts.mu.Lock()
	ts.sessionStart = time.Now()
	ts.mu.Unlock()
	ts.currentAtomic.Store(trafficSnapshot{})
	// OPT #5: инициализируем клиент один раз — Transport с keep-alive пулом.
	ts.client = http.Client{Timeout: 0}
	for {
		ts.connect(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

func (ts *trafficStore) connect(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, config.ClashAPIBase+"/traffic", nil)
	if err != nil {
		return
	}
	// OPT #5: используем переиспользуемый клиент (инициализирован в run()).
	resp, err := ts.client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var snap trafficSnapshot
		if err := json.Unmarshal(line, &snap); err != nil {
			continue
		}
		// Атомарное обновление снапшота — lock-free для читателей.
		ts.currentAtomic.Store(snap)
		ts.lastOK.Store(time.Now().UnixNano())
		// Счётчики сессии: редкая запись — мьютекс оправдан.
		ts.mu.Lock()
		ts.sessionUpB += snap.Up
		ts.sessionDnB += snap.Down
		ts.mu.Unlock()
	}
}

func (ts *trafficStore) get() (trafficSnapshot, bool) {
	snap, _ := ts.currentAtomic.Load().(trafficSnapshot)
	lastNs := ts.lastOK.Load()
	ok := lastNs > 0 && time.Since(time.Unix(0, lastNs)) < 10*time.Second
	return snap, ok
}

func (ts *trafficStore) getSessionTotals() (upB, dnB int64, dur float64) {
	ts.mu.Lock()
	upB, dnB = ts.sessionUpB, ts.sessionDnB
	start := ts.sessionStart
	ts.mu.Unlock()
	return upB, dnB, time.Since(start).Seconds()
}

// ── Per-outbound speed via cumulative delta ───────────────────────────────────

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
	prev     map[string]connSample
	speeds   map[string]outboundSpeed
	active   atomic.Int64
	// BUG FIX #16: apiAvailable отделяет "0 активных соединений" от "API недоступен".
	// Ранее active=0 при ошибке не отличалось от active=0 при отсутствии соединений.
	apiAvailable atomic.Bool
	lastTick time.Time
}

func (ct *connSpeedTracker) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
		ct.tick(ctx)
	}
}

func (ct *connSpeedTracker) tick(ctx context.Context) {
	conns, err := fetchConnectionsData(ctx)
	// BUG FIX #16: сначала фиксируем доступность API, затем обновляем счётчик.
	// Так можно отличить "API недоступен" от "0 активных соединений" на стороне UI.
	if err != nil {
		ct.apiAvailable.Store(false)
		ct.active.Store(0)
		ct.mu.Lock()
		for k := range ct.speeds {
			delete(ct.speeds, k)
		}
		ct.mu.Unlock()
		return
	}
	ct.apiAvailable.Store(true)
	ct.active.Store(int64(len(conns)))
	if len(conns) == 0 {
		ct.mu.Lock()
		for k := range ct.speeds {
			delete(ct.speeds, k)
		}
		ct.mu.Unlock()
		return
	}

	now := time.Now()
	ct.mu.Lock()
	defer ct.mu.Unlock()

	dt := now.Sub(ct.lastTick).Seconds()
	if dt < 0.1 || ct.lastTick.IsZero() {
		ct.lastTick = now
		ct.prev = buildSamples(conns)
		return
	}
	ct.lastTick = now

	// Переиспользуем maps вместо аллокации новых каждые 2 секунды.
	// make(map) каждые 2с = ~24 аллокации/мин → GC pressure.
	newSpeeds := make(map[string]outboundSpeed, len(ct.speeds)+2)
	newPrev := buildSamples(conns)

	for _, c := range conns {
		key := connKeyFor(c)
		prev, existed := ct.prev[key]
		if !existed {
			continue
		}
		dUp := c.Upload - prev.upload
		dDn := c.Download - prev.download
		if dUp < 0 {
			dUp = 0
		}
		if dDn < 0 {
			dDn = 0
		}
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
	if c.ID != "" {
		return c.ID
	}
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
	Up      int64   `json:"up"`
	Down    int64   `json:"down"`
	ProxyUp int64   `json:"proxy_up"`
	ProxyDn int64   `json:"proxy_dn"`
	DirUp   int64   `json:"direct_up"`
	DirDn   int64   `json:"direct_dn"`
	Active  int     `json:"active_connections"`
	OK      bool    `json:"ok"`
	// BUG FIX #16: APIAvailable отличает "sing-box API недоступен" от "0 соединений".
	APIAvailable bool    `json:"api_available"`
	SessUpB int64   `json:"sess_up_bytes"`
	SessDnB int64   `json:"sess_dn_bytes"`
	SessSec float64 `json:"sess_duration_sec"`
}

func (h *DiagHandlers) handleStats(w http.ResponseWriter, _ *http.Request) {
	snap, ok := h.traffic.get()
	resp := StatsResponse{
		Up:           snap.Up,
		Down:         snap.Down,
		OK:           ok,
		Active:       int(h.conns.active.Load()),
		APIAvailable: h.conns.apiAvailable.Load(),
	}
	resp.ProxyUp, resp.ProxyDn, resp.DirUp, resp.DirDn = h.conns.getSpeeds()
	resp.SessUpB, resp.SessDnB, resp.SessSec = h.traffic.getSessionTotals()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// ── Connections struct ────────────────────────────────────────────────────────

type clashConn struct {
	ID       string   `json:"id"`
	Outbound string   `json:"outbound"`
	Chains   []string `json:"chains"`
	Upload   int64    `json:"upload"`
	Download int64    `json:"download"`
	Metadata struct {
		Host          string `json:"host"`
		DestinationIP string `json:"destinationIP"`
		Network       string `json:"network"`
		ProcessPath   string `json:"processPath"`
	} `json:"metadata"`
	Rule        string `json:"rule"`
	RulePayload string `json:"rulePayload"`
}

func (c *clashConn) effectiveOutbound() string {
	if c.Outbound != "" {
		return c.Outbound
	}
	if len(c.Chains) > 0 && c.Chains[0] != "" {
		return c.Chains[0]
	}
	return ""
}

func fetchConnectionsData(ctx context.Context) ([]clashConn, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, config.ClashAPIBase+"/connections", nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 2 * time.Second}
	r, err := client.Do(req)
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

func (h *DiagHandlers) handleDebugStats(w http.ResponseWriter, r *http.Request) {
	conns, err := fetchConnectionsData(r.Context())
	snap, snapOK := h.traffic.get()

	h.conns.mu.RLock()
	speeds := make(map[string]interface{})
	for k, v := range h.conns.speeds {
		speeds[k] = map[string]int64{"up": v.UpSpeed, "dn": v.DnSpeed}
	}
	prevCount := len(h.conns.prev)
	h.conns.mu.RUnlock()

	sample := []map[string]interface{}{}
	for i, c := range conns {
		if i >= 5 {
			break
		}
		sample = append(sample, map[string]interface{}{
			"id":       c.ID,
			"outbound": c.Outbound,
			"upload":   c.Upload,
			"download": c.Download,
			"host":     c.Metadata.Host,
			"proc":     c.Metadata.ProcessPath,
			"chains":   c.Chains,
			"rule":     c.Rule,
			"network":  c.Metadata.Network,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"traffic_snap":       snap,
		"traffic_snap_ok":    snapOK,
		"speeds_by_outbound": speeds,
		"prev_count":         prevCount,
		"total_conns":        len(conns),
		"err": func() string {
			if err != nil {
				return err.Error()
			}
			return ""
		}(),
		"sample_conns": sample,
	})
}

func (h *DiagHandlers) handleConnections(w http.ResponseWriter, r *http.Request) {
	conns, err := fetchConnectionsData(r.Context())
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

func handleDiagTest(w http.ResponseWriter, r *http.Request) {
	proxyURL, _ := url.Parse("http://" + config.ProxyAddr)
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
