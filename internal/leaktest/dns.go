package leaktest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	DefaultDNSProbeCount = 5
	DefaultHTTPTimeout   = 8 * time.Second
)

// DNSConfig describes the controlled DNS leak test endpoint.
type DNSConfig struct {
	Domain            string
	ReportURL         string
	ExpectedResolvers []string
	ProbeCount        int
	HTTPClient        *http.Client
	LookupHost        func(context.Context, string) ([]string, error)
	Sleep             func(context.Context, time.Duration) error
}

type DNSLeakReport struct {
	TestID            string    `json:"test_id"`
	Resolvers         []string  `json:"resolvers"`
	ExpectedResolvers []string  `json:"expected_resolvers"`
	Leaked            bool      `json:"leaked"`
	Status            string    `json:"status"`
	CheckedAt         time.Time `json:"checked_at"`
}

// RunDNSLeakTest performs DNS probes and asks the leak-test service which resolvers saw them.
func RunDNSLeakTest(ctx context.Context, cfg DNSConfig) (*DNSLeakReport, error) {
	if strings.TrimSpace(cfg.Domain) == "" {
		return nil, fmt.Errorf("dns leak test domain is required")
	}
	if strings.TrimSpace(cfg.ReportURL) == "" {
		return nil, fmt.Errorf("dns leak report URL is required")
	}
	if err := requireHTTPS(cfg.ReportURL); err != nil {
		return nil, fmt.Errorf("report_url: %w", err)
	}
	count := cfg.ProbeCount
	if count <= 0 {
		count = DefaultDNSProbeCount
	}
	lookup := cfg.LookupHost
	if lookup == nil {
		resolver := net.DefaultResolver
		lookup = resolver.LookupHost
	}
	sleep := cfg.Sleep
	if sleep == nil {
		sleep = sleepContext
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: DefaultHTTPTimeout}
	}
	testID, err := newTestID()
	if err != nil {
		return nil, err
	}
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("%s-%d.%s", testID, i+1, strings.TrimSuffix(cfg.Domain, "."))
		_, _ = lookup(ctx, name)
	}
	if err := sleep(ctx, 5*time.Second); err != nil {
		return nil, err
	}
	resolvers, err := fetchResolvers(ctx, client, cfg.ReportURL, testID)
	if err != nil {
		return nil, err
	}
	sort.Strings(resolvers)
	report := &DNSLeakReport{
		TestID:            testID,
		Resolvers:         resolvers,
		ExpectedResolvers: append([]string(nil), cfg.ExpectedResolvers...),
		CheckedAt:         time.Now().UTC(),
		Status:            "protected",
	}
	report.Leaked = hasUnexpectedResolver(resolvers, cfg.ExpectedResolvers)
	if report.Leaked {
		report.Status = "leak"
	}
	if len(resolvers) == 0 {
		report.Status = "unknown"
	}
	return report, nil
}

func fetchResolvers(ctx context.Context, client *http.Client, baseURL, testID string) ([]string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + url.PathEscape(testID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch DNS leak report: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("fetch DNS leak report: HTTP %d", resp.StatusCode)
	}
	var body struct {
		Resolvers []string `json:"resolvers"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode DNS leak report: %w", err)
	}
	return normalizeIPs(body.Resolvers), nil
}

func hasUnexpectedResolver(got, expected []string) bool {
	if len(got) == 0 {
		return false
	}
	allow := map[string]bool{}
	for _, ip := range normalizeIPs(expected) {
		allow[ip] = true
	}
	if len(allow) == 0 {
		return true
	}
	for _, ip := range normalizeIPs(got) {
		if !allow[ip] {
			return true
		}
	}
	return false
}

func normalizeIPs(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, raw := range in {
		ip := net.ParseIP(strings.TrimSpace(raw))
		if ip == nil {
			continue
		}
		s := ip.String()
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func newTestID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate DNS leak test id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func requireHTTPS(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("https URL required")
	}
	return nil
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
