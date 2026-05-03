package mtu

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestDetectFindsHighestWorkingMTU(t *testing.T) {
	got, err := Detect(context.Background(), 1280, 1500, func(_ context.Context, size int) error {
		if size > 1420 {
			return fmt.Errorf("too large")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if got != 1420 {
		t.Fatalf("Detect = %d, want 1420", got)
	}
}

func TestDetectStopsOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Detect(ctx, 1280, 1500, func(context.Context, int) error { return nil }); err == nil {
		t.Fatal("Detect succeeded with canceled context")
	}
}

func TestCacheLookupStoresPerServerMTU(t *testing.T) {
	cache := NewCache(filepath.Join(t.TempDir(), "mtu_cache.json"), time.Hour)
	if err := cache.Store("Example.COM", 51820, 1420); err != nil {
		t.Fatalf("Store: %v", err)
	}
	got, ok := cache.Lookup("example.com", 51820)
	if !ok {
		t.Fatal("Lookup missed stored MTU")
	}
	if got != 1420 {
		t.Fatalf("Lookup = %d, want 1420", got)
	}
}

func TestCacheLookupExpiresOldEntries(t *testing.T) {
	cache := NewCache(filepath.Join(t.TempDir(), "mtu_cache.json"), time.Hour)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cache.now = func() time.Time { return now }
	if err := cache.Store("example.com", 51820, 1420); err != nil {
		t.Fatalf("Store: %v", err)
	}
	cache.now = func() time.Time { return now.Add(2 * time.Hour) }
	if _, ok := cache.Lookup("example.com", 51820); ok {
		t.Fatal("Lookup returned expired MTU")
	}
}
