package subscription

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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
	defaultPollInterval = 30 * time.Minute
	initialBackoff      = 5 * time.Minute
	maxBackoff          = time.Hour
)

type Subscription struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	URL         string        `json:"url,omitempty"`
	UpdateEvery time.Duration `json:"update_every"`
	UserAgent   string        `json:"user_agent,omitempty"`
	LastUpdated time.Time     `json:"last_updated,omitempty"`
	LastAttempt time.Time     `json:"last_attempt,omitempty"`
	NextAttempt time.Time     `json:"next_attempt,omitempty"`
	Servers     []ServerEntry `json:"servers,omitempty"`
	Quota       Quota         `json:"quota,omitempty"`
	Backoff     time.Duration `json:"backoff,omitempty"`
	LastError   string        `json:"last_error,omitempty"`
	Empty       bool          `json:"empty,omitempty"`
	CreatedAt   time.Time     `json:"created_at,omitempty"`
}

type UpdateResult struct {
	Added    int           `json:"added"`
	Removed  int           `json:"removed"`
	Changed  int           `json:"changed"`
	Servers  []ServerEntry `json:"servers"`
	Quota    Quota         `json:"quota,omitempty"`
	Warnings []string      `json:"warnings,omitempty"`
}

type ApplyFunc func(ctx context.Context, sub Subscription, result UpdateResult) error

type Options struct {
	Dir          string
	Client       *http.Client
	IsSupported  func(string) bool
	ApplyServers ApplyFunc
	PollInterval time.Duration
	Now          func() time.Time
}

type Manager struct {
	mu           sync.RWMutex
	dir          string
	client       *http.Client
	isSupported  func(string) bool
	applyServers ApplyFunc
	pollInterval time.Duration
	now          func() time.Time
	subs         map[string]*Subscription
}

type metaFile struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	URL             string    `json:"url"`
	UpdateEveryNsec int64     `json:"update_every_nsec"`
	UserAgent       string    `json:"user_agent,omitempty"`
	LastUpdated     time.Time `json:"last_updated,omitempty"`
	LastAttempt     time.Time `json:"last_attempt,omitempty"`
	NextAttempt     time.Time `json:"next_attempt,omitempty"`
	Quota           Quota     `json:"quota,omitempty"`
	BackoffNsec     int64     `json:"backoff_nsec,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
	Empty           bool      `json:"empty,omitempty"`
	CreatedAt       time.Time `json:"created_at,omitempty"`
}

func NewManager(opts Options) (*Manager, error) {
	if opts.Dir == "" {
		return nil, fmt.Errorf("subscription dir is required")
	}
	if opts.Client == nil {
		opts.Client = &http.Client{Timeout: 20 * time.Second}
	}
	if opts.IsSupported == nil {
		return nil, fmt.Errorf("isSupported is required")
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = defaultPollInterval
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	m := &Manager{
		dir:          opts.Dir,
		client:       opts.Client,
		isSupported:  opts.IsSupported,
		applyServers: opts.ApplyServers,
		pollInterval: opts.PollInterval,
		now:          opts.Now,
		subs:         map[string]*Subscription{},
	}
	if err := os.MkdirAll(opts.Dir, 0755); err != nil {
		return nil, fmt.Errorf("create subscription dir: %w", err)
	}
	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) Add(s *Subscription) error {
	if s == nil {
		return fmt.Errorf("subscription is required")
	}
	s.URL = strings.TrimSpace(s.URL)
	if err := validateHTTPSURL(s.URL); err != nil {
		return err
	}
	if s.ID == "" {
		id, err := newID()
		if err != nil {
			return err
		}
		s.ID = id
	}
	if s.Name = strings.TrimSpace(s.Name); s.Name == "" {
		s.Name = "Subscription"
	}
	if s.CreatedAt.IsZero() {
		s.CreatedAt = m.now().UTC()
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.subs[s.ID]; exists {
		return fmt.Errorf("subscription %s already exists", s.ID)
	}
	cp := cloneSubscription(s)
	m.subs[cp.ID] = &cp
	return m.saveLocked(&cp)
}

func (m *Manager) Remove(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.subs[id]; !exists {
		return fmt.Errorf("subscription %s not found", id)
	}
	delete(m.subs, id)
	if err := os.Remove(m.metaPath(id)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove subscription meta: %w", err)
	}
	if err := os.Remove(m.serversPath(id)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove subscription servers: %w", err)
	}
	return nil
}

func (m *Manager) List() []*Subscription {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Subscription, 0, len(m.subs))
	for _, s := range m.subs {
		cp := cloneSubscription(s)
		out = append(out, &cp)
	}
	return out
}

func (m *Manager) UpdateNow(ctx context.Context, id string) (*UpdateResult, error) {
	m.mu.RLock()
	sub, ok := m.subs[id]
	if !ok {
		m.mu.RUnlock()
		return nil, fmt.Errorf("subscription %s not found", id)
	}
	snapshot := cloneSubscription(sub)
	m.mu.RUnlock()

	result, err := m.fetch(ctx, snapshot)
	now := m.now().UTC()

	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.subs[id]
	if !ok {
		return nil, fmt.Errorf("subscription %s not found", id)
	}
	current.LastAttempt = now
	if err != nil {
		current.Backoff = nextBackoff(current.Backoff)
		current.NextAttempt = now.Add(current.Backoff)
		current.LastError = err.Error()
		if saveErr := m.saveLocked(current); saveErr != nil {
			return nil, saveErr
		}
		return nil, err
	}
	if len(result.Servers) == 0 {
		current.Empty = true
		current.LastError = "subscription returned no supported servers"
		current.NextAttempt = now.Add(nextBackoff(current.Backoff))
		if saveErr := m.saveLocked(current); saveErr != nil {
			return nil, saveErr
		}
		return &result, fmt.Errorf("subscription returned no supported servers")
	}

	result.Added, result.Removed, result.Changed = diffServers(current.Servers, result.Servers)
	current.Servers = cloneServers(result.Servers)
	current.Quota = result.Quota
	current.LastUpdated = now
	current.NextAttempt = nextDue(now, current.UpdateEvery)
	current.Backoff = 0
	current.LastError = ""
	current.Empty = false
	if saveErr := m.saveLocked(current); saveErr != nil {
		return nil, saveErr
	}
	if m.applyServers != nil {
		if applyErr := m.applyServers(ctx, cloneSubscription(current), result); applyErr != nil {
			return nil, fmt.Errorf("apply subscription servers: %w", applyErr)
		}
	}
	return &result, nil
}

func (m *Manager) Start(ctx context.Context) {
	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()
	m.updateDue(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.updateDue(ctx)
		}
	}
}

func (m *Manager) updateDue(ctx context.Context) {
	now := m.now().UTC()
	var ids []string
	m.mu.RLock()
	for id, sub := range m.subs {
		if sub.UpdateEvery <= 0 {
			continue
		}
		next := sub.NextAttempt
		if next.IsZero() {
			next = nextDue(sub.LastUpdated, sub.UpdateEvery)
		}
		if !next.After(now) {
			ids = append(ids, id)
		}
	}
	m.mu.RUnlock()
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(subID string) {
			defer wg.Done()
			_, _ = m.UpdateNow(ctx, subID)
		}(id)
	}
	wg.Wait()
}

func (m *Manager) fetch(ctx context.Context, sub Subscription) (UpdateResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sub.URL, nil)
	if err != nil {
		return UpdateResult{}, fmt.Errorf("create subscription request: %w", err)
	}
	if sub.UserAgent != "" {
		req.Header.Set("User-Agent", sub.UserAgent)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return UpdateResult{}, fmt.Errorf("download subscription: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return UpdateResult{}, fmt.Errorf("subscription HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes+1))
	if err != nil {
		return UpdateResult{}, fmt.Errorf("read subscription: %w", err)
	}
	if len(body) > MaxBodyBytes {
		return UpdateResult{}, fmt.Errorf("subscription response exceeds 1 MiB")
	}
	parsed := ParseBody(body, m.isSupported)
	return UpdateResult{
		Servers:  parsed.Servers,
		Quota:    ParseUserInfoHeader(resp.Header.Get("subscription-userinfo")),
		Warnings: parsed.Warnings,
	}, nil
}

func (m *Manager) load() error {
	entries, err := filepath.Glob(filepath.Join(m.dir, "*.json"))
	if err != nil {
		return fmt.Errorf("list subscriptions: %w", err)
	}
	for _, path := range entries {
		if strings.HasSuffix(path, ".servers.json") {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read subscription meta: %w", err)
		}
		var meta metaFile
		if err := json.Unmarshal(data, &meta); err != nil {
			return fmt.Errorf("parse subscription meta: %w", err)
		}
		rawURL, err := decryptString(meta.URL)
		if err != nil {
			return fmt.Errorf("decrypt subscription URL: %w", err)
		}
		sub := &Subscription{
			ID:          meta.ID,
			Name:        meta.Name,
			URL:         rawURL,
			UpdateEvery: time.Duration(meta.UpdateEveryNsec),
			UserAgent:   meta.UserAgent,
			LastUpdated: meta.LastUpdated,
			LastAttempt: meta.LastAttempt,
			NextAttempt: meta.NextAttempt,
			Quota:       meta.Quota,
			Backoff:     time.Duration(meta.BackoffNsec),
			LastError:   meta.LastError,
			Empty:       meta.Empty,
			CreatedAt:   meta.CreatedAt,
		}
		servers, err := m.loadServers(sub.ID)
		if err != nil {
			return err
		}
		sub.Servers = servers
		m.subs[sub.ID] = sub
	}
	return nil
}

func (m *Manager) saveLocked(s *Subscription) error {
	if err := os.MkdirAll(m.dir, 0755); err != nil {
		return fmt.Errorf("create subscription dir: %w", err)
	}
	encURL, err := encryptString(s.URL)
	if err != nil {
		return fmt.Errorf("encrypt subscription URL: %w", err)
	}
	meta := metaFile{
		ID:              s.ID,
		Name:            s.Name,
		URL:             encURL,
		UpdateEveryNsec: int64(s.UpdateEvery),
		UserAgent:       s.UserAgent,
		LastUpdated:     s.LastUpdated,
		LastAttempt:     s.LastAttempt,
		NextAttempt:     s.NextAttempt,
		Quota:           s.Quota,
		BackoffNsec:     int64(s.Backoff),
		LastError:       s.LastError,
		Empty:           s.Empty,
		CreatedAt:       s.CreatedAt,
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal subscription meta: %w", err)
	}
	if err := fileutil.WriteAtomic(m.metaPath(s.ID), data, 0644); err != nil {
		return fmt.Errorf("write subscription meta: %w", err)
	}
	servers, err := json.MarshalIndent(s.Servers, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal subscription servers: %w", err)
	}
	if err := fileutil.WriteAtomic(m.serversPath(s.ID), servers, 0644); err != nil {
		return fmt.Errorf("write subscription servers: %w", err)
	}
	return nil
}

func (m *Manager) loadServers(id string) ([]ServerEntry, error) {
	data, err := os.ReadFile(m.serversPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read subscription servers: %w", err)
	}
	var servers []ServerEntry
	if err := json.Unmarshal(data, &servers); err != nil {
		return nil, fmt.Errorf("parse subscription servers: %w", err)
	}
	return servers, nil
}

func (m *Manager) metaPath(id string) string {
	return filepath.Join(m.dir, id+".json")
}

func (m *Manager) serversPath(id string) string {
	return filepath.Join(m.dir, id+".servers.json")
}

func validateHTTPSURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("invalid subscription URL")
	}
	if u.Scheme != "https" {
		return fmt.Errorf("subscription URL must use https")
	}
	return nil
}

func encryptString(value string) (string, error) {
	encrypted, err := dpapi.Encrypt([]byte(value))
	if err != nil {
		return "", err
	}
	return "DPAPI:" + base64.StdEncoding.EncodeToString(encrypted), nil
}

func decryptString(value string) (string, error) {
	if !strings.HasPrefix(value, "DPAPI:") {
		return value, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, "DPAPI:"))
	if err != nil {
		return "", fmt.Errorf("decode dpapi payload: %w", err)
	}
	plain, err := dpapi.Decrypt(raw)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func newID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate subscription id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func cloneSubscription(s *Subscription) Subscription {
	cp := *s
	cp.Servers = cloneServers(s.Servers)
	return cp
}

func cloneServers(in []ServerEntry) []ServerEntry {
	if len(in) == 0 {
		return nil
	}
	out := make([]ServerEntry, len(in))
	copy(out, in)
	return out
}

func diffServers(oldServers, newServers []ServerEntry) (added, removed, changed int) {
	oldByKey := map[string]ServerEntry{}
	newByKey := map[string]ServerEntry{}
	for _, s := range oldServers {
		oldByKey[serverKey(s)] = s
	}
	for _, s := range newServers {
		newByKey[serverKey(s)] = s
	}
	for key, next := range newByKey {
		prev, ok := oldByKey[key]
		if !ok {
			added++
			continue
		}
		if !bytes.Equal([]byte(prev.URI), []byte(next.URI)) || prev.Name != next.Name {
			changed++
		}
	}
	for key := range oldByKey {
		if _, ok := newByKey[key]; !ok {
			removed++
		}
	}
	return added, removed, changed
}

func serverKey(s ServerEntry) string {
	if parsed, err := url.Parse(s.URI); err == nil && parsed.Host != "" {
		user := ""
		if parsed.User != nil {
			user = parsed.User.String()
		}
		return strings.ToLower(parsed.Scheme + "://" + user + "@" + parsed.Host)
	}
	return s.URI
}

func nextBackoff(current time.Duration) time.Duration {
	if current <= 0 {
		return initialBackoff
	}
	next := current * 2
	if next > maxBackoff {
		return maxBackoff
	}
	return next
}

func nextDue(base time.Time, interval time.Duration) time.Time {
	if interval <= 0 {
		return time.Time{}
	}
	if base.IsZero() {
		return time.Time{}
	}
	return base.Add(interval)
}
