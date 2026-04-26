package speedtest

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"time"
)

type Result struct {
	DownloadMbps float64 `json:"download_mbps"`
	LatencyMs    int64   `json:"latency_ms"`
	Error        string  `json:"error,omitempty"`
}

func Run(ctx context.Context, proxyAddr string) Result {
	start := time.Now()
	transport := &http.Transport{}
	if proxyAddr != "" {
		if proxyURL, err := url.Parse("http://" + proxyAddr); err == nil {
			transport.Proxy = http.ProxyURL(proxyURL)
		}
	}
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://speed.cloudflare.com/__down?bytes=10000000", nil)
	if err != nil {
		return Result{Error: err.Error()}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{Error: err.Error()}
	}
	defer resp.Body.Close()
	latency := time.Since(start).Milliseconds()
	n, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		return Result{LatencyMs: latency, Error: err.Error()}
	}
	elapsed := time.Since(start).Seconds()
	if elapsed <= 0 {
		elapsed = 1
	}
	return Result{DownloadMbps: float64(n) / elapsed / 125000, LatencyMs: latency}
}
