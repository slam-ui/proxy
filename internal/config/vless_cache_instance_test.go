package config

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestVLESSCache_IsolatedInstances проверяет что два параллельных теста с разными
// VLESSCache{} инстансами не влияют друг на друга (A-9).
func TestVLESSCache_IsolatedInstances(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()

	pathA := filepath.Join(dirA, "secret_a.key")
	pathB := filepath.Join(dirB, "secret_b.key")

	urlA := "vless://uuid-aaa@server-a.example.com:443?sni=a.example.com&pbk=pubkey-a&sid=sid-a"
	urlB := "vless://uuid-bbb@server-b.example.com:443?sni=b.example.com&pbk=pubkey-b&sid=sid-b"

	if err := os.WriteFile(pathA, []byte(urlA), 0644); err != nil {
		t.Fatalf("WriteFile A: %v", err)
	}
	if err := os.WriteFile(pathB, []byte(urlB), 0644); err != nil {
		t.Fatalf("WriteFile B: %v", err)
	}

	var cacheA, cacheB VLESSCache

	var wg sync.WaitGroup
	errCh := make(chan error, 4)

	// Горутина А читает из cacheA
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			p, err := cacheA.Parse(pathA)
			if err != nil {
				errCh <- err
				return
			}
			if p.Address != "server-a.example.com" {
				errCh <- nil
				t.Errorf("cacheA вернул address=%q, want server-a.example.com", p.Address)
			}
		}
	}()

	// Горутина B читает из cacheB
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			p, err := cacheB.Parse(pathB)
			if err != nil {
				errCh <- err
				return
			}
			if p.Address != "server-b.example.com" {
				t.Errorf("cacheB вернул address=%q, want server-b.example.com", p.Address)
			}
		}
	}()

	// Горутина инвалидирует cacheA — не должна влиять на cacheB
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 3; i++ {
			cacheA.Invalidate()
		}
	}()

	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Errorf("горутина вернула ошибку: %v", err)
		}
	}
}

// TestVLESSCache_ZeroValue проверяет что zero-value VLESSCache корректно работает.
func TestVLESSCache_ZeroValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.key")
	url := "vless://uuid-zero@server-zero.example.com:443?sni=zero.example.com&pbk=pubkey-zero&sid=sid-zero"

	if err := os.WriteFile(path, []byte(url), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var cache VLESSCache // zero-value
	p, err := cache.Parse(path)
	if err != nil {
		t.Fatalf("Parse на zero-value VLESSCache: %v", err)
	}
	if p.Address != "server-zero.example.com" {
		t.Errorf("address = %q, want server-zero.example.com", p.Address)
	}
}

// TestVLESSCache_GlobalNotAffectedByLocalInvalidate проверяет что инвалидация
// локального кэша не влияет на глобальный defaultVLESSCache.
func TestVLESSCache_GlobalNotAffectedByLocalInvalidate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret_global.key")
	url := "vless://uuid-global@server-global.example.com:443?sni=global.example.com&pbk=pubkey-global&sid=sid-global"

	if err := os.WriteFile(path, []byte(url), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Прогреваем глобальный кэш
	p1, err := parseVLESSKey(path)
	if err != nil {
		t.Fatalf("parseVLESSKey: %v", err)
	}

	// Инвалидируем ЛОКАЛЬНЫЙ кэш
	var localCache VLESSCache
	localCache.Invalidate()

	// Глобальный кэш должен остаться нетронутым
	p2, err := parseVLESSKey(path)
	if err != nil {
		t.Fatalf("parseVLESSKey после локальной инвалидации: %v", err)
	}
	if p1.Address != p2.Address {
		t.Errorf("глобальный кэш повреждён: p1.Address=%q, p2.Address=%q", p1.Address, p2.Address)
	}
}
