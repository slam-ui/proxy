package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"proxyclient/internal/config"
	"proxyclient/internal/logger"
)

// geositeUpdateMeta хранит метаданные последнего обновления geosite файлов.
type geositeUpdateMeta struct {
	LastChecked time.Time `json:"last_checked"`
	LastUpdated time.Time `json:"last_updated"`
}

// GeoAutoUpdater периодически проверяет возраст geosite файлов и обновляет устаревшие.
// B-10: аналог Clash Verge Rev geodata auto-update / v2rayN «Update Geo files».
type GeoAutoUpdater struct {
	mu             sync.Mutex
	ctx            context.Context
	cancel         context.CancelFunc
	running        bool
	log            logger.Logger
	updateMetaFile string
	interval       time.Duration // порог устаревания и интервал тика (default 7 дней)
	triggerCh      chan struct{}  // небуферизованный сигнал для TriggerNow()

	// downloadFn — внедрение зависимости: позволяет тестам заменить реальную загрузку.
	downloadFn func(name string) error
}

// NewGeoAutoUpdater создаёт GeoAutoUpdater с заданным интервалом обновления.
// Если interval <= 0 — используется 7 дней по умолчанию.
func NewGeoAutoUpdater(log logger.Logger, interval time.Duration) *GeoAutoUpdater {
	if interval <= 0 {
		interval = 7 * 24 * time.Hour
	}
	return &GeoAutoUpdater{
		log:            log,
		updateMetaFile: filepath.Join(config.DataDir, "geosite_updated.json"),
		interval:       interval,
		triggerCh:      make(chan struct{}, 1),
		downloadFn:     downloadGeositeFile,
	}
}

// Start запускает периодическую проверку geosite файлов в отдельной горутине.
// Повторный вызов без Stop ничего не делает.
func (g *GeoAutoUpdater) Start(ctx context.Context) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.running {
		return
	}
	g.ctx, g.cancel = context.WithCancel(ctx)
	g.running = true
	go g.loop()
}

// Stop останавливает updater.
func (g *GeoAutoUpdater) Stop() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.running && g.cancel != nil {
		g.cancel()
		g.running = false
	}
}

// IsRunning возвращает true если updater запущен.
func (g *GeoAutoUpdater) IsRunning() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.running
}

// TriggerNow запускает немедленную проверку без ожидания тика.
// Не блокируется: если проверка уже в очереди — пропускает.
func (g *GeoAutoUpdater) TriggerNow() {
	select {
	case g.triggerCh <- struct{}{}:
	default:
	}
}

// LoadMeta читает метаданные последнего обновления. Возвращает нулевую структуру при ошибке.
func (g *GeoAutoUpdater) LoadMeta() geositeUpdateMeta {
	data, err := os.ReadFile(g.updateMetaFile)
	if err != nil {
		return geositeUpdateMeta{}
	}
	var m geositeUpdateMeta
	_ = json.Unmarshal(data, &m)
	return m
}

// loop — основной цикл: сразу проверяет при старте, затем по тику.
func (g *GeoAutoUpdater) loop() {
	// Проверка при старте — не ждём первого тика.
	g.checkAndUpdate()

	ticker := time.NewTicker(g.interval)
	defer ticker.Stop()

	for {
		select {
		case <-g.ctx.Done():
			return
		case <-ticker.C:
			g.checkAndUpdate()
		case <-g.triggerCh:
			g.checkAndUpdate()
		}
	}
}

// checkAndUpdate проверяет возраст всех geosite-*.bin в DataDir и обновляет устаревшие.
func (g *GeoAutoUpdater) checkAndUpdate() {
	pattern := filepath.Join(config.DataDir, "geosite-*.bin")
	entries, err := filepath.Glob(pattern)
	if err != nil || len(entries) == 0 {
		return
	}

	updated := false
	for _, path := range entries {
		// Прерываемся при отмене контекста.
		select {
		case <-g.ctx.Done():
			return
		default:
		}

		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if time.Since(info.ModTime()) <= g.interval {
			continue // файл ещё свежий
		}

		// Извлекаем имя: "data/geosite-youtube.bin" → "youtube"
		base := filepath.Base(path) // "geosite-youtube.bin"
		name := base[len("geosite-") : len(base)-len(".bin")]

		g.log.Info("B-10: geosite-%s устарел (возраст: %v) — обновляем...",
			name, time.Since(info.ModTime()).Truncate(time.Hour))

		if err := g.downloadFn(name); err != nil {
			g.log.Warn("B-10: не удалось обновить geosite-%s: %v", name, err)
		} else {
			g.log.Info("B-10: geosite-%s обновлён ✓", name)
			updated = true
		}
	}

	g.saveMeta(updated)
}

// saveMeta атомарно записывает время последней проверки/обновления.
func (g *GeoAutoUpdater) saveMeta(updated bool) {
	existing := g.LoadMeta()
	now := time.Now()
	meta := geositeUpdateMeta{
		LastChecked: now,
		LastUpdated: existing.LastUpdated,
	}
	if updated {
		meta.LastUpdated = now
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return
	}
	_ = os.WriteFile(g.updateMetaFile, data, 0644)
}

// geoAutoUpdateSettings — настройки автообновления geosite, хранятся в файле.
type geoAutoUpdateSettings struct {
	Enabled      bool `json:"enabled"`
	IntervalDays int  `json:"interval_days"`
}

const geoSettingsFile = config.DataDir + "/geosite_settings.json"

// loadGeoAutoUpdateSettings читает настройки из файла. При ошибке — defaults.
func loadGeoAutoUpdateSettings() geoAutoUpdateSettings {
	data, err := os.ReadFile(geoSettingsFile)
	if err != nil {
		return geoAutoUpdateSettings{Enabled: true, IntervalDays: 7}
	}
	var s geoAutoUpdateSettings
	if err := json.Unmarshal(data, &s); err != nil {
		return geoAutoUpdateSettings{Enabled: true, IntervalDays: 7}
	}
	if s.IntervalDays <= 0 {
		s.IntervalDays = 7
	}
	return s
}

// saveGeoAutoUpdateSettings сохраняет настройки в файл.
func saveGeoAutoUpdateSettings(s geoAutoUpdateSettings) error {
	if err := os.MkdirAll(config.DataDir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(geoSettingsFile, data, 0644)
}
