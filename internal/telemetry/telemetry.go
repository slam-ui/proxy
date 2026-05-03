package telemetry

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"proxyclient/internal/dpapi"
	"proxyclient/internal/fileutil"
)

const (
	DefaultEndpointPath = "/api/telemetry/v1"
	MaxEventsPerBatch   = 100
	MaxRequestBytes     = 100 << 10
)

type Event struct {
	Type            string    `json:"type"`
	Timestamp       time.Time `json:"ts"`
	Protocol        string    `json:"protocol,omitempty"`
	Transport       string    `json:"transport,omitempty"`
	Code            string    `json:"code,omitempty"`
	Stage           string    `json:"stage,omitempty"`
	DurationSeconds int64     `json:"duration_seconds,omitempty"`
	BytesUp         int64     `json:"bytes_up,omitempty"`
	BytesDown       int64     `json:"bytes_down,omitempty"`
}

type Payload struct {
	AnonymousID          string  `json:"anonymous_id"`
	ClientVersion        string  `json:"client_version"`
	OSVersion            string  `json:"os_version"`
	Locale               string  `json:"locale"`
	Events               []Event `json:"events"`
	SessionUptimeSeconds int64   `json:"session_uptime_seconds"`
}

type Buffer struct {
	mu       sync.Mutex
	capacity int
	events   []Event
}

func NewBuffer(capacity int) *Buffer {
	if capacity <= 0 {
		capacity = MaxEventsPerBatch
	}
	return &Buffer{capacity: capacity, events: make([]Event, 0, capacity)}
}

func (b *Buffer) Add(event Event) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, event)
	return len(b.events) >= b.capacity
}

func (b *Buffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.events)
}

func (b *Buffer) Drain(max int) []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	if max <= 0 || max > len(b.events) {
		max = len(b.events)
	}
	out := append([]Event(nil), b.events[:max]...)
	copy(b.events, b.events[max:])
	b.events = b.events[:len(b.events)-max]
	return out
}

type Client struct {
	Enabled       bool
	BaseURL       string
	AnonymousPath string
	HTTPClient    *http.Client
	UserAgent     string
}

func (c Client) EnsureAnonymousID() (string, error) {
	if !c.Enabled {
		return "", nil
	}
	if c.AnonymousPath == "" {
		return "", fmt.Errorf("anonymous id path is required")
	}
	if data, err := os.ReadFile(c.AnonymousPath); err == nil && len(data) > 0 {
		plain, err := dpapi.Decrypt(data)
		if err != nil {
			return "", fmt.Errorf("decrypt anonymous id: %w", err)
		}
		id := strings.TrimSpace(string(plain))
		if id != "" {
			return id, nil
		}
	} else if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("read anonymous id: %w", err)
	}
	id, err := newUUIDv4()
	if err != nil {
		return "", err
	}
	encrypted, err := dpapi.Encrypt([]byte(id))
	if err != nil {
		return "", fmt.Errorf("encrypt anonymous id: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(c.AnonymousPath), 0700); err != nil {
		return "", fmt.Errorf("create anonymous id dir: %w", err)
	}
	if err := fileutil.WriteAtomic(c.AnonymousPath, encrypted, 0600); err != nil {
		return "", fmt.Errorf("write anonymous id: %w", err)
	}
	return id, nil
}

func (c Client) Flush(ctx context.Context, payload Payload) error {
	if !c.Enabled {
		return nil
	}
	if len(payload.Events) == 0 {
		return nil
	}
	if len(payload.Events) > MaxEventsPerBatch {
		payload.Events = payload.Events[:MaxEventsPerBatch]
	}
	endpoint, err := telemetryURL(c.BaseURL)
	if err != nil {
		return err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode telemetry payload: %w", err)
	}
	if len(body) > MaxRequestBytes {
		return fmt.Errorf("telemetry payload too large: %d bytes", len(body))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send telemetry: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("send telemetry: HTTP %d", resp.StatusCode)
	}
	return nil
}

func telemetryURL(baseURL string) (string, error) {
	if strings.TrimSpace(baseURL) == "" {
		return "", fmt.Errorf("telemetry base URL is required")
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("absolute telemetry base URL required")
	}
	u.Path = strings.TrimRight(u.Path, "/") + DefaultEndpointPath
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func newUUIDv4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate uuid: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return hex.EncodeToString(b[0:4]) + "-" +
		hex.EncodeToString(b[4:6]) + "-" +
		hex.EncodeToString(b[6:8]) + "-" +
		hex.EncodeToString(b[8:10]) + "-" +
		hex.EncodeToString(b[10:16]), nil
}
