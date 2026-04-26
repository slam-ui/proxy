package api

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"proxyclient/internal/config"
	"proxyclient/internal/fileutil"
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
	doneCh         chan struct{} // закрывается когда горутина loop() реально завершилась
	log            logger.Logger
	updateMetaFile string
	interval       time.Duration // порог устаревания и интервал тика (default 7 дней)
	triggerCh      chan struct{} // буферизованный (cap=1) сигнал для TriggerNow() — не блокирует отправителя
	// БАГ 3: задержка первой проверки при старте — XRay может быть в процессе restart.
	startupDelay time.Duration
	// BUG-5 FIX: onUpdated вызывается после успешного обновления хотя бы одного файла.
	// Устанавливается через SetOnUpdated — позволяет app.go подключить TriggerApply
	// без циклической зависимости между geositeupdate и tun_handlers.
	onUpdated func()

	// downloadFn — внедрение зависимости: позволяет тестам заменить реальную загрузку.
	downloadFn func(ctx context.Context, name string) error
	// targetNamesFn возвращает geosite-имена, которые действительно используются в routing rules.
	targetNamesFn func() []string
}

// SetOnUpdated регистрирует callback который будет вызван после успешного обновления
// хотя бы одного geosite файла. Безопасно вызывать до Start().
func (g *GeoAutoUpdater) SetOnUpdated(fn func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.onUpdated = fn
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
		// БАГ 3: 30-секундная задержка при старте — XRay может быть в процессе restart
		// и ещё не слушать порт 10807. TriggerNow() обходит задержку.
		startupDelay: 30 * time.Second,
		downloadFn: func(ctx context.Context, name string) error {
			return downloadGeositeFile(ctx, name)
		},
		targetNamesFn: func() []string {
			routing, err := config.LoadRoutingConfig(filepath.Join(config.DataDir, "routing.json"))
			if err != nil {
				return nil
			}
			return geositeRuleNamesFromConfig(routing)
		},
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
	g.doneCh = make(chan struct{})
	go func() {
		defer close(g.doneCh)
		defer func() {
			g.mu.Lock()
			g.running = false
			g.mu.Unlock()
		}()
		g.loop()
	}()
}

// Stop останавливает updater и ждёт реального завершения горутины loop().
// Гарантирует что после возврата нет активных HTTP-запросов или записей в файлы.
func (g *GeoAutoUpdater) Stop() {
	g.mu.Lock()
	if !g.running {
		g.mu.Unlock()
		return
	}
	if g.cancel != nil {
		g.cancel()
	}
	done := g.doneCh
	g.mu.Unlock()
	// Ждём реального завершения горутины — не просто отмены контекста.
	// Без этого быстрый Stop+Start запускает две горутины одновременно.
	if done != nil {
		<-done
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

// loop — основной цикл.
// БАГ 3: вместо немедленного вызова checkAndUpdate() при старте ждём startupDelay.
// XRay может быть в процессе graceful restart и не слушать порт → proxyconnect WARN burst.
// TriggerNow() обходит задержку (отправляет в triggerCh), так что ручное обновление
// из UI работает без ожидания.
func (g *GeoAutoUpdater) loop() {
	// Первый шаг: ждём startupDelay или TriggerNow().
	select {
	case <-time.After(g.startupDelay):
		g.checkAndUpdate()
	case <-g.triggerCh:
		g.checkAndUpdate()
	case <-g.ctx.Done():
		return
	}

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

// checkAndUpdate проверяет возраст geosite-файлов, которые используются в routing rules.
func (g *GeoAutoUpdater) checkAndUpdate() {
	// Защита от вызова до Start() — ctx может быть nil.
	if g.ctx == nil {
		return
	}
	names := []string(nil)
	if g.targetNamesFn != nil {
		names = g.targetNamesFn()
	}
	if len(names) == 0 {
		return
	}

	updated := false
	for _, name := range names {
		// Прерываемся при отмене контекста.
		select {
		case <-g.ctx.Done():
			return
		default:
		}

		path := filepath.Join(config.DataDir, "geosite-"+name+".bin")
		info, err := os.Stat(path)
		missing := os.IsNotExist(err)
		if err != nil && !missing {
			g.log.Warn("B-10: geosite-%s: не удалось проверить файл: %v", name, err)
			continue
		}
		// Файл обновляем если он отсутствует или устарел. Повреждённые/не-SRS файлы
		// отфильтровываются при GenerateSingBoxConfig; mtime не игнорируем, чтобы
		// ErrGeositeNotFound не вызывал повторную попытку на каждом тике.
		stale := !missing && time.Since(info.ModTime()) > g.interval
		needsUpdate := missing || stale
		if !needsUpdate {
			continue
		}

		if missing {
			g.log.Info("B-10: geosite-%s отсутствует, но есть в правилах — скачиваем...", name)
		} else {
			g.log.Info("B-10: geosite-%s устарел (возраст: %v) — обновляем...",
				name, time.Since(info.ModTime()).Truncate(time.Hour))
		}

		if err := g.downloadFn(g.ctx, name); err != nil {
			// БАГ 4d: если все источники вернули 404 — файл недоступен в природе.
			// Обновляем mtime чтобы подавить повторную попытку на следующий interval (7 дней).
			// Иначе: файл не обновлён → mtime старый → через 7 дней снова 404 WARN (бесконечный спам).
			if errors.Is(err, ErrGeositeNotFound) {
				now := time.Now()
				if missing {
					g.log.Info("B-10: geosite-%s недоступен (404 во всех источниках) — файл отсутствует", name)
				} else if chtimesErr := os.Chtimes(path, now, now); chtimesErr != nil {
					g.log.Warn("B-10: geosite-%s: не удалось обновить mtime (%v) — следующий запуск повторит попытку", name, chtimesErr)
				} else {
					g.log.Info("B-10: geosite-%s недоступен (404 во всех источниках) — пропускаем на %v", name, g.interval.Truncate(time.Hour))
				}
			} else {
				g.log.Warn("B-10: не удалось обновить geosite-%s: %v", name, err)
			}
		} else {
			g.log.Info("B-10: geosite-%s обновлён ✓", name)
			updated = true
		}
	}

	// BUG-5 FIX: уведомляем подписчика (TriggerApply) после обновления файлов.
	// Без этого sing-box работает со старыми geosite правилами до следующего ручного рестарта.
	if updated {
		g.mu.Lock()
		cb := g.onUpdated
		g.mu.Unlock()
		if cb != nil {
			cb()
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
	// FIX 51: атомарная запись метаданных — предотвращает повреждение файла при сбое.
	_ = fileutil.WriteAtomic(g.updateMetaFile, data, 0644)
}

// geoAutoUpdateSettings — настройки автообновления geosite, хранятся в файле.
type geoAutoUpdateSettings struct {
	Enabled      bool `json:"enabled"`
	IntervalDays int  `json:"interval_days"`
}

// geoSettingsFile — путь к файлу настроек автообновления geosite.
// Использует filepath.Join вместо конкатенации "/" для корректной работы на Windows.
var geoSettingsFile = filepath.Join(config.DataDir, "geosite_settings.json")

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
	// FIX 51: атомарная запись — предотвращает частичные записи при сбое.
	return fileutil.WriteAtomic(geoSettingsFile, data, 0644)
}
