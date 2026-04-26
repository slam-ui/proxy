package keepalive

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

func Run(ctx context.Context, proxyAddr string, interval time.Duration) {
	if interval <= 0 {
		interval = 120 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	client := buildProxyClient(proxyAddr)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			req, err := http.NewRequestWithContext(ctx, http.MethodHead, "http://connectivitycheck.gstatic.com/generate_204", nil)
			if err != nil {
				continue
			}
			resp, err := client.Do(req)
			if err == nil {
				resp.Body.Close()
			}
		}
	}
}

func buildProxyClient(addr string) *http.Client {
	transport := &http.Transport{}
	if addr != "" {
		if proxyURL, err := url.Parse("http://" + addr); err == nil {
			transport.Proxy = http.ProxyURL(proxyURL)
		}
	}
	return &http.Client{Transport: transport, Timeout: 10 * time.Second}
}
