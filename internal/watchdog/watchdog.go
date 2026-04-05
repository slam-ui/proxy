// Package watchdog мониторит стабильность прямого WireGuard соединения.
//
// Алгоритм:
//  1. Каждую секунду проверяет соединение (TCP probe к серверу).
//  2. Считает обрывы в скользящем окне 30 секунд.
//  3. Если 3+ обрыва за 30 сек — вызывает OnUnstable.
//  4. Когда соединение восстанавливается — вызывает OnStable.
//
// Используется для автоматического переключения на TURN туннель.
package watchdog

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"proxyclient/internal/logger"
)

const (
	// ProbeInterval — интервал между проверками соединения.
	ProbeInterval = 1 * time.Second
	// ProbeTimeout — таймаут одной TCP probe.
	ProbeTimeout = 3 * time.Second
	// Window — размер скользящего окна для подсчёта обрывов.
	Window = 30 * time.Second
	// Threshold — количество обрывов в Window для срабатывания OnUnstable.
	Threshold = 3
	// RecoveryProbes — сколько успешных проб подряд нужно для вызова OnStable.
	//
	// BUG FIX: было 3 (3 секунды).
	// ТСПУ/DPI не блокирует TCP handshake — только содержимое VLESS трафика.
	// При 3 пробах watchdog видел «соединение восстановлено» через 3 секунды
	// после включения TURN и немедленно вызывал SwitchToDirect, хотя DPI
	// всё ещё активен. TURN отключался через 2-3 секунды после включения.
	// Увеличено до 60: требуем 60 секунд стабильного TCP прежде чем
	// сигнализировать о восстановлении через watchdog.
	// Основной возврат на direct происходит через retryLoop (каждые 15 минут).
	RecoveryProbes = 60
)

// State — текущее состояние соединения.
type State int

const (
	StateUnknown  State = iota
	StateDirect         // прямое соединение стабильно
	StateUnstable       // нестабильно, будет переключение на TURN
)

// Config — настройки watchdog.
type Config struct {
	// Target — адрес сервера для проверки (host:port), например "1.2.3.4:51820".
	Target string
	// ProbeInterval переопределяет интервал по умолчанию (опционально).
	ProbeInterval time.Duration
	// ДОП-8: двухуровневая проба для обнаружения DPI-блокировки.
	// DPI/ТСПУ пропускают TCP handshake но блокируют VLESS трафик.
	// Если ProxyAddr и ProbeURL заданы — после успешной TCP-пробы выполняется
	// HTTP-запрос через локальный прокси; провал HTTP = DPI-блокировка.
	//
	// ProxyAddr — адрес локального HTTP прокси sing-box, например "127.0.0.1:10807".
	ProxyAddr string
	// ProbeURL — URL для HTTP-пробы, например "http://detectportal.firefox.com/".
	// Должен быть простым и быстрым хостом без DPI-исключений.
	ProbeURL string
	// OnUnstable вызывается когда соединение признаётся нестабильным.
	// Вызывается один раз до восстановления.
	OnUnstable func()
	// OnStable вызывается когда соединение восстановилось после нестабильности.
	// Вызывается только после RecoveryProbes (60) успешных проб подряд.
	OnStable func()
	// Logger для диагностики.
	Logger logger.Logger
}

// Watchdog следит за соединением и сообщает об изменениях стабильности.
type Watchdog struct {
	cfg      Config
	mu       sync.Mutex
	state    State
	failures []time.Time // временны́е метки обрывов в окне
	okStreak int         // успешных проб подряд (для восстановления)
	log      logger.Logger
}

// New создаёт Watchdog с заданной конфигурацией.
func New(cfg Config) *Watchdog {
	interval := cfg.ProbeInterval
	if interval == 0 {
		interval = ProbeInterval
	}
	cfg.ProbeInterval = interval

	log := cfg.Logger
	if log == nil {
		log = logger.NewNop()
	}

	return &Watchdog{cfg: cfg, log: log}
}

// Run запускает мониторинг и блокирует до отмены ctx.
func (w *Watchdog) Run(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.ProbeInterval)
	defer ticker.Stop()

	w.log.Info("watchdog: старт мониторинга → %s (порог: %d обрывов за %s)",
		w.cfg.Target, Threshold, Window)

	for {
		select {
		case <-ctx.Done():
			w.log.Info("watchdog: остановка")
			return
		case <-ticker.C:
			w.probe()
		}
	}
}

// probe выполняет одну TCP (и опционально HTTP) проверку и обновляет состояние.
func (w *Watchdog) probe() {
	ok := tcpProbe(w.cfg.Target, ProbeTimeout)
	// ДОП-8: если TCP успешен но настроена HTTP-проба — проверяем VLESS трафик.
	// DPI пропускает TCP handshake но может блокировать содержимое.
	if ok && w.cfg.ProxyAddr != "" && w.cfg.ProbeURL != "" {
		if !httpProbe(w.cfg.ProxyAddr, w.cfg.ProbeURL, ProbeTimeout) {
			w.log.Info("watchdog: TCP ок, но HTTP probe через прокси провалилась → DPI-блокировка")
			ok = false
		}
	}
	now := time.Now()

	w.mu.Lock()
	defer w.mu.Unlock()

	if ok {
		w.okStreak++
		// Восстановление: RecoveryProbes успешных проб подряд после нестабильности.
		// RecoveryProbes = 60 — защита от ложных срабатываний при ТСПУ/DPI:
		// DPI пропускает TCP handshake, но блокирует VLESS трафик.
		// Требуем 60 секунд стабильного TCP прежде чем считать соединение восстановленным.
		if w.state == StateUnstable && w.okStreak >= RecoveryProbes {
			w.log.Info("watchdog: соединение восстановлено (%d успешных проб подряд)", w.okStreak)
			w.state = StateDirect
			w.failures = nil
			if w.cfg.OnStable != nil {
				go w.cfg.OnStable()
			}
		}
		return
	}

	// Сбой пробы.
	w.okStreak = 0
	w.log.Info("watchdog: probe failed → %s", w.cfg.Target)

	// Отсекаем старые события за пределами окна.
	cutoff := now.Add(-Window)
	valid := w.failures[:0]
	for _, t := range w.failures {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	w.failures = append(valid, now)

	w.log.Info("watchdog: обрывов в окне %s: %d/%d", Window, len(w.failures), Threshold)

	if w.state != StateUnstable && len(w.failures) >= Threshold {
		w.log.Info("watchdog: порог достигнут — соединение нестабильно")
		w.state = StateUnstable
		if w.cfg.OnUnstable != nil {
			go w.cfg.OnUnstable()
		}
	}
}

// State возвращает текущее состояние (потокобезопасно).
func (w *Watchdog) State() State {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state
}

// tcpProbe пытается установить TCP соединение с target за timeout.
// Возвращает true если соединение успешно.
func tcpProbe(target string, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", target, timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// httpProbe выполняет HTTP GET через локальный прокси (sing-box inbound).
// Возвращает true если получен любой ответ (status < 500).
// Используется для обнаружения DPI: TCP может быть ок, но VLESS трафик заблокирован.
func httpProbe(proxyAddr, probeURL string, timeout time.Duration) bool {
	transport := &http.Transport{
		Proxy: func(*http.Request) (*url.URL, error) {
			return url.Parse("http://" + proxyAddr)
		},
		DialContext:           (&net.Dialer{Timeout: timeout}).DialContext,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse // не следуем редиректам — нам нужен только статус
		},
	}
	resp, err := client.Get(probeURL)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 500
}
