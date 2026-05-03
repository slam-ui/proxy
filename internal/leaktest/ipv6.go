package leaktest

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"proxyclient/internal/netutil"
)

type IPv6Report struct {
	Available bool      `json:"available"`
	Address   string    `json:"address,omitempty"`
	Leaked    bool      `json:"leaked"`
	Status    string    `json:"status"`
	CheckedAt time.Time `json:"checked_at"`
}

func RunIPv6LeakTest(ctx context.Context, client *http.Client) (*IPv6Report, error) {
	if client == nil {
		client = netutil.SharedHTTPClient(DefaultHTTPTimeout)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api6.ipify.org?format=text", nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return &IPv6Report{Available: false, Status: "unavailable", CheckedAt: time.Now().UTC()}, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &IPv6Report{Available: false, Status: "unavailable", CheckedAt: time.Now().UTC()}, nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 128))
	if err != nil {
		return nil, fmt.Errorf("read IPv6 response: %w", err)
	}
	addr := strings.TrimSpace(string(data))
	ip := net.ParseIP(addr)
	if ip == nil || ip.To4() != nil {
		return nil, fmt.Errorf("invalid IPv6 response %q", addr)
	}
	return &IPv6Report{
		Available: true,
		Address:   ip.String(),
		Leaked:    true,
		Status:    "ipv6_available",
		CheckedAt: time.Now().UTC(),
	}, nil
}
