package api

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"proxyclient/internal/logger"
)

// TestGeoAutoUpdater_DownloadsStaleFile проверяет что updater обновляет файл
// возраст которого превышает интервал (8 дней > 7 дней).
func TestGeoAutoUpdater_DownloadsStaleFile(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer os.Chdir(old)

	// Создаём data/ и устаревший geosite файл
	if err := os.MkdirAll("data", 0755); err != nil {
		t.Fatalf("MkdirAll data/: %v", err)
	}
	stalePath := filepath.Join("data", "geosite-youtube.bin")
	if err := os.WriteFile(stalePath, []byte("old-data"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// Устанавливаем mtime 8 дней назад
	staleTime := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(stalePath, staleTime, staleTime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	// Mock-функция загрузки
	var mu sync.Mutex
	var downloaded []string
	mockDownload := func(name string) error {
		mu.Lock()
		downloaded = append(downloaded, name)
		mu.Unlock()
		return nil
	}

	g := NewGeoAutoUpdater(&logger.NoOpLogger{}, 7*24*time.Hour)
	g.downloadFn = mockDownload

	// Вручную устанавливаем контекст и вызываем checkAndUpdate синхронно
	g.ctx, g.cancel = context.WithCancel(context.Background())
	g.checkAndUpdate()

	mu.Lock()
	n := len(downloaded)
	mu.Unlock()

	if n == 0 {
		t.Fatal("ожидалось обновление geosite-youtube, но загрузка не была инициирована")
	}
	if downloaded[0] != "youtube" {
		t.Errorf("downloaded[0]=%q, ожидалось %q", downloaded[0], "youtube")
	}
}

// TestGeoAutoUpdater_SkipsFreshFile проверяет что свежие файлы не обновляются.
func TestGeoAutoUpdater_SkipsFreshFile(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer os.Chdir(old)

	if err := os.MkdirAll("data", 0755); err != nil {
		t.Fatalf("MkdirAll data/: %v", err)
	}
	freshPath := filepath.Join("data", "geosite-discord.bin")
	if err := os.WriteFile(freshPath, []byte("fresh-data"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// mtime = 1 день назад (интервал 7 дней → не устарел)
	freshTime := time.Now().Add(-24 * time.Hour)
	os.Chtimes(freshPath, freshTime, freshTime)

	var mu sync.Mutex
	var downloaded []string
	mockDownload := func(name string) error {
		mu.Lock()
		downloaded = append(downloaded, name)
		mu.Unlock()
		return nil
	}

	g := NewGeoAutoUpdater(&logger.NoOpLogger{}, 7*24*time.Hour)
	g.downloadFn = mockDownload
	g.ctx, g.cancel = context.WithCancel(context.Background())
	g.checkAndUpdate()

	mu.Lock()
	n := len(downloaded)
	mu.Unlock()

	if n != 0 {
		t.Errorf("ожидалось 0 загрузок для свежего файла, получено %d", n)
	}
}

// TestGeoAutoUpdater_SaveLoadMeta проверяет сохранение и чтение метаданных.
func TestGeoAutoUpdater_SaveLoadMeta(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer os.Chdir(old)

	os.MkdirAll("data", 0755)

	g := NewGeoAutoUpdater(&logger.NoOpLogger{}, 7*24*time.Hour)
	g.ctx, g.cancel = context.WithCancel(context.Background())

	before := time.Now().Truncate(time.Second)
	g.saveMeta(true)

	meta := g.LoadMeta()
	if meta.LastUpdated.Before(before) {
		t.Errorf("LastUpdated=%v должно быть >= %v", meta.LastUpdated, before)
	}
	if meta.LastChecked.Before(before) {
		t.Errorf("LastChecked=%v должно быть >= %v", meta.LastChecked, before)
	}
}

// TestGeoAutoUpdateSettings_LoadSave проверяет сохранение/загрузку настроек.
func TestGeoAutoUpdateSettings_LoadSave(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer os.Chdir(old)

	os.MkdirAll("data", 0755)

	// Defaults при отсутствии файла
	s := loadGeoAutoUpdateSettings()
	if !s.Enabled {
		t.Error("default Enabled должен быть true")
	}
	if s.IntervalDays != 7 {
		t.Errorf("default IntervalDays=%d, ожидалось 7", s.IntervalDays)
	}

	// Сохраняем и перечитываем
	if err := saveGeoAutoUpdateSettings(geoAutoUpdateSettings{Enabled: false, IntervalDays: 14}); err != nil {
		t.Fatalf("saveGeoAutoUpdateSettings: %v", err)
	}
	s2 := loadGeoAutoUpdateSettings()
	if s2.Enabled {
		t.Error("Enabled должен быть false после сохранения")
	}
	if s2.IntervalDays != 14 {
		t.Errorf("IntervalDays=%d, ожидалось 14", s2.IntervalDays)
	}
}
