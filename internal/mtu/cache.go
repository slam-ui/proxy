package mtu

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"proxyclient/internal/fileutil"
)

const DefaultCacheTTL = 30 * 24 * time.Hour

type Cache struct {
	mu   sync.Mutex
	path string
	ttl  time.Duration
	now  func() time.Time
}

type Entry struct {
	Key       string    `json:"key"`
	Server    string    `json:"server"`
	Port      int       `json:"port"`
	MTU       int       `json:"mtu"`
	UpdatedAt time.Time `json:"updated_at"`
}

type cacheFile struct {
	Entries []Entry `json:"entries"`
}

func NewCache(path string, ttl time.Duration) *Cache {
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	return &Cache{path: path, ttl: ttl, now: time.Now}
}

func (c *Cache) Lookup(server string, port int) (int, bool) {
	if c == nil || strings.TrimSpace(c.path) == "" {
		return 0, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	file := c.loadLocked()
	key := cacheKey(server, port)
	now := c.now().UTC()
	for _, entry := range file.Entries {
		if entry.Key != key {
			continue
		}
		if entry.UpdatedAt.IsZero() || now.Sub(entry.UpdatedAt) > c.ttl {
			return 0, false
		}
		return Clamp(entry.MTU, DefaultWireGuard), true
	}
	return 0, false
}

func (c *Cache) Store(server string, port, value int) error {
	if c == nil || strings.TrimSpace(c.path) == "" {
		return fmt.Errorf("mtu cache path is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	file := c.loadLocked()
	key := cacheKey(server, port)
	entry := Entry{
		Key:       key,
		Server:    strings.TrimSpace(strings.ToLower(server)),
		Port:      port,
		MTU:       Clamp(value, DefaultWireGuard),
		UpdatedAt: c.now().UTC(),
	}
	replaced := false
	for i := range file.Entries {
		if file.Entries[i].Key == key {
			file.Entries[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		file.Entries = append(file.Entries, entry)
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal MTU cache: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0755); err != nil {
		return fmt.Errorf("create MTU cache dir: %w", err)
	}
	if err := fileutil.WriteAtomic(c.path, data, 0644); err != nil {
		return fmt.Errorf("write MTU cache: %w", err)
	}
	return nil
}

func (c *Cache) loadLocked() cacheFile {
	data, err := os.ReadFile(c.path)
	if err != nil {
		return cacheFile{}
	}
	var file cacheFile
	if err := json.Unmarshal(data, &file); err != nil {
		return cacheFile{}
	}
	return file
}

func cacheKey(server string, port int) string {
	return fmt.Sprintf("%s:%d", strings.TrimSpace(strings.ToLower(server)), port)
}
