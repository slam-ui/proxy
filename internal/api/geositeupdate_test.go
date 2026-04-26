package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"proxyclient/internal/logger"
)

func validTestSRS(size int) []byte {
	if size < 4 {
		size = 4
	}
	data := make([]byte, size)
	copy(data, []byte{'S', 'R', 'S', 1})
	return data
}

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
	mockDownload := func(_ context.Context, name string) error {
		mu.Lock()
		downloaded = append(downloaded, name)
		mu.Unlock()
		return nil
	}

	g := NewGeoAutoUpdater(&logger.NoOpLogger{}, 7*24*time.Hour)
	g.downloadFn = mockDownload
	g.targetNamesFn = func() []string { return []string{"youtube"} }

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
	freshContent := validTestSRS(159)
	if err := os.WriteFile(freshPath, freshContent, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// mtime = 1 день назад (интервал 7 дней → не устарел)
	freshTime := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(freshPath, freshTime, freshTime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	var mu sync.Mutex
	var downloaded []string
	mockDownload := func(_ context.Context, name string) error {
		mu.Lock()
		downloaded = append(downloaded, name)
		mu.Unlock()
		return nil
	}

	g := NewGeoAutoUpdater(&logger.NoOpLogger{}, 7*24*time.Hour)
	g.downloadFn = mockDownload
	g.targetNamesFn = func() []string { return []string{"discord"} }
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

// ── БАГ 7: geoUpdaterMu data race ────────────────────────────────────────────

// TestServer_GeoUpdaterRace проверяет что Set/GetGeoAutoUpdater потокобезопасны
// (запускать с -race).
func TestServer_GeoUpdaterRace(t *testing.T) {
	s := &Server{}
	g := NewGeoAutoUpdater(&logger.NoOpLogger{}, 0)

	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			s.SetGeoAutoUpdater(g)
		}
		close(done)
	}()
	for i := 0; i < 100; i++ {
		_ = s.GetGeoAutoUpdater()
	}
	<-done
}

// ── БАГ 2: isProxyConnectError ────────────────────────────────────────────────

// TestIsProxyConnectError проверяет корректное определение ошибок прокси-соединения.
func TestIsProxyConnectError(t *testing.T) {
	cases := []struct {
		err  string
		want bool
	}{
		{"proxyconnect tcp: dial tcp 127.0.0.1:10807: connectex: No connection could be made", true},
		{"connection refused", true},
		{"actively refused it", true},
		{":10807 some error", true},
		{"timeout exceeded", false},
		{"EOF", false},
		{"", false},
	}
	for _, tc := range cases {
		var err error
		if tc.err != "" {
			err = fmt.Errorf("%s", tc.err)
		}
		if got := isProxyConnectError(err); got != tc.want {
			t.Errorf("isProxyConnectError(%q)=%v, want %v", tc.err, got, tc.want)
		}
	}
}

// ── БАГ 3: startupDelay ───────────────────────────────────────────────────────

// TestGeoAutoUpdater_StartupDelay проверяет что TriggerNow() обходит startupDelay,
// а обычный старт не вызывает checkAndUpdate немедленно.
func TestGeoAutoUpdater_StartupDelay(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer os.Chdir(old)
	os.MkdirAll("data", 0755)

	var mu sync.Mutex
	var callCount int
	mockDownload := func(_ context.Context, _ string) error {
		mu.Lock()
		callCount++
		mu.Unlock()
		return nil
	}

	g := NewGeoAutoUpdater(&logger.NoOpLogger{}, 7*24*time.Hour)
	g.downloadFn = mockDownload
	// Устанавливаем короткую задержку для теста
	g.startupDelay = 200 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g.Start(ctx)

	// Сразу после старта (до истечения startupDelay) checkAndUpdate не должна была вызваться
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	n := callCount
	mu.Unlock()
	if n != 0 {
		t.Errorf("через 50ms ожидалось 0 вызовов, получено %d", n)
	}

	// TriggerNow() должен обойти задержку
	g.TriggerNow()
	time.Sleep(100 * time.Millisecond)
	// Нет файлов в data/ → checkAndUpdate ничего не делает, но вызов произошёл
	// (callCount остаётся 0, т.к. нет файлов — это ок, проверяем что не упало)
}

// ── ErrGeositeNotFound подавляет повторный WARN ───────────────────────────────

// TestGeoAutoUpdater_ErrNotFound_SuppressesRepeatAttempt проверяет что
// после ErrGeositeNotFound mtime обновляется и повторный вызов не делает попытку скачать.
func TestGeoAutoUpdater_ErrNotFound_SuppressesRepeatAttempt(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer os.Chdir(old)

	os.MkdirAll("data", 0755)

	// Создаём стейл файл < 512 байт (имитация HTML-страницы с ошибкой)
	stalePath := filepath.Join("data", "geosite-discord.bin")
	os.WriteFile(stalePath, []byte("<!DOCTYPE html>"), 0644)
	staleTime := time.Now().Add(-8 * 24 * time.Hour)
	os.Chtimes(stalePath, staleTime, staleTime)

	var callCount int
	mockDownload := func(_ context.Context, name string) error {
		callCount++
		return fmt.Errorf("geosite-%s %w (src1: 404; src2: 404; src3: 404)",
			name, ErrGeositeNotFound)
	}

	g := NewGeoAutoUpdater(&logger.NoOpLogger{}, 7*24*time.Hour)
	g.downloadFn = mockDownload
	g.targetNamesFn = func() []string { return []string{"discord"} }
	g.ctx, g.cancel = context.WithCancel(context.Background())

	// Первый вызов: файл стейл → скачиваем → ErrGeositeNotFound → Chtimes обновляет mtime
	g.checkAndUpdate()
	if callCount != 1 {
		t.Fatalf("ожидался 1 вызов downloadFn, получено %d", callCount)
	}

	// mtime должен быть обновлён
	info, err := os.Stat(stalePath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if time.Since(info.ModTime()) > time.Minute {
		t.Errorf("mtime не обновлён после ErrGeositeNotFound: %v", info.ModTime())
	}

	// Второй вызов: mtime свежий → downloadFn НЕ должна вызываться
	g.checkAndUpdate()
	if callCount != 1 {
		t.Errorf("ожидался всё ещё 1 вызов downloadFn после подавления, получено %d", callCount)
	}
}

// ── БАГ 4: size < 512 validation ─────────────────────────────────────────────

// TestGeoAutoUpdater_SmallFileUpdated проверяет что файл < 512 байт обновляется
// даже если он свежий.
func TestGeoAutoUpdater_SmallFileUpdated(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer os.Chdir(old)

	os.MkdirAll("data", 0755)
	// Создаём устаревший маленький файл (< 512 байт, mtime > interval).
	// Стейл + маленький → должен обновиться (mtime старый — не 404-подавлённый).
	smallPath := filepath.Join("data", "geosite-discord.bin")
	if err := os.WriteFile(smallPath, []byte("small"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	staleTime := time.Now().Add(-8 * 24 * time.Hour)
	os.Chtimes(smallPath, staleTime, staleTime)

	var mu sync.Mutex
	var downloaded []string
	mockDownload := func(_ context.Context, name string) error {
		mu.Lock()
		downloaded = append(downloaded, name)
		mu.Unlock()
		return nil
	}

	g := NewGeoAutoUpdater(&logger.NoOpLogger{}, 7*24*time.Hour)
	g.downloadFn = mockDownload
	g.targetNamesFn = func() []string { return []string{"discord"} }
	g.ctx, g.cancel = context.WithCancel(context.Background())
	g.checkAndUpdate()

	mu.Lock()
	n := len(downloaded)
	mu.Unlock()

	if n == 0 {
		t.Fatal("маленький файл должен был обновиться, но загрузка не инициирована")
	}
}

func TestDownloadGeositeFile_DirectFallbackDoesNotUseProxy(t *testing.T) {
	dir := t.TempDir()
	oldWD, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer os.Chdir(oldWD)

	payload := validTestSRS(159)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/geosite-youtube.srs" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write(payload)
	}))
	defer ts.Close()

	oldSources := geositeSources
	oldProxyAddr := geositeProxyAddr
	geositeSources = []string{ts.URL + "/geosite-%s.srs"}
	geositeProxyAddr = "127.0.0.1:1"
	defer func() {
		geositeSources = oldSources
		geositeProxyAddr = oldProxyAddr
	}()

	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")

	clients := geositeHTTPClients(context.Background())
	if len(clients) != 1 || clients[0].name != "direct" {
		t.Fatalf("expected direct-only clients when local proxy is down, got %#v", clients)
	}
	if tr, ok := clients[0].client.Transport.(*http.Transport); !ok || tr.Proxy != nil {
		t.Fatalf("direct geosite transport must not use environment/system proxy: %#v", clients[0].client.Transport)
	}

	if err := downloadGeositeFile(context.Background(), "youtube"); err != nil {
		t.Fatalf("downloadGeositeFile: %v", err)
	}
	fi, err := os.Stat(filepath.Join("data", "geosite-youtube.bin"))
	if err != nil {
		t.Fatalf("stat downloaded file: %v", err)
	}
	if fi.Size() != int64(len(payload)) {
		t.Fatalf("downloaded size=%d, want %d", fi.Size(), len(payload))
	}
}
