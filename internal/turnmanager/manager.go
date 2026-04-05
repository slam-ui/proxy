// Package turnmanager управляет переключением между прямым соединением
// и TURN туннелем (маскировка под звонок VK через DTLS 1.2).
//
// Схема работы:
//
//	прямое соединение: WireGuard → сервер напрямую
//	TURN туннель:      WireGuard → 127.0.0.1:9000 (vk-turn-proxy) → TURN сервер → сервер
//
// Переключение происходит:
//   - На TURN: watchdog сообщает о нестабильности (3+ обрыва за 30 сек)
//   - Обратно на прямое: каждые 5 минут проверяем, доступен ли прямой путь
package turnmanager

import (
	"context"
	"sync"
	"time"

	"proxyclient/internal/logger"
	"proxyclient/internal/notification"
)

const (
	// TURNProxyAddr — адрес локального vk-turn-proxy процесса.
	// WireGuard peer перенаправляется сюда в режиме TURN.
	TURNProxyAddr = "127.0.0.1:9000"

	// RetryDirectInterval — интервал проверки прямого соединения в TURN режиме.
	RetryDirectInterval = 5 * time.Minute

	// DirectCheckTimeout — таймаут при проверке прямого пути.
	DirectCheckTimeout = 10 * time.Second
)

// Mode — активный режим соединения.
type Mode int

const (
	ModeDirect Mode = iota // WireGuard → сервер напрямую
	ModeTURN               // WireGuard → vk-turn-proxy → TURN → сервер
)

func (m Mode) String() string {
	switch m {
	case ModeDirect:
		return "direct"
	case ModeTURN:
		return "TURN"
	default:
		return "unknown"
	}
}

// SwitchFn — функция, которую Manager вызывает для смены режима.
// Реализация должна перегенерировать конфиг и перезапустить sing-box.
// Возвращает ошибку если переключение не удалось.
type SwitchFn func(mode Mode) error

// DirectCheckFn — функция проверки доступности прямого соединения.
// Используется перед возвратом с TURN на direct.
type DirectCheckFn func(ctx context.Context) bool

// Config — настройки Manager.
type Config struct {
	// OnSwitch вызывается при каждой смене режима (direct ↔ TURN).
	OnSwitch SwitchFn
	// CheckDirect проверяет доступность прямого соединения.
	// Если nil — используется встроенная заглушка (всегда false).
	CheckDirect DirectCheckFn
	// RetryInterval переопределяет RetryDirectInterval (опционально).
	RetryInterval time.Duration
	// Logger для диагностики.
	Logger logger.Logger
}

// Manager управляет режимом соединения и периодически проверяет
// возможность вернуться на прямое подключение.
type Manager struct {
	cfg    Config
	mu     sync.Mutex
	mode   Mode
	manual bool // true — TURN включён вручную пользователем; retryLoop не возвращает на direct
	log    logger.Logger
	cancel context.CancelFunc // отменяет горутину retryLoop
}

// New создаёт Manager.
func New(cfg Config) *Manager {
	interval := cfg.RetryInterval
	if interval == 0 {
		interval = RetryDirectInterval
	}
	cfg.RetryInterval = interval

	log := cfg.Logger
	if log == nil {
		log = logger.NewNop()
	}

	if cfg.CheckDirect == nil {
		cfg.CheckDirect = func(_ context.Context) bool { return false }
	}

	return &Manager{cfg: cfg, log: log, mode: ModeDirect}
}

// Mode возвращает текущий режим (потокобезопасно).
func (m *Manager) Mode() Mode {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mode
}

// SwitchToTURNManual переключает на TURN туннель в ручном режиме.
// В отличие от SwitchToTURN, retryLoop не запускается — возврат на direct
// происходит только когда пользователь явно вызовет SwitchToDirect.
// Безопасен для вызова из нескольких горутин.
func (m *Manager) SwitchToTURNManual() {
	m.mu.Lock()
	alreadyTURN := m.mode == ModeTURN
	m.mode = ModeTURN
	m.manual = true
	m.mu.Unlock()

	// Если retryLoop уже работал (запущен авто-переключением) — останавливаем:
	// в ручном режиме автоматический возврат нежелателен.
	m.stopRetryLoop()

	if !alreadyTURN {
		m.log.Info("turnmanager: переключение на TURN туннель вручную (%s)", TURNProxyAddr)
		notification.Send("Proxy", "Обход белых списков включён (ручной режим)")

		if m.cfg.OnSwitch != nil {
			if err := m.cfg.OnSwitch(ModeTURN); err != nil {
				m.log.Error("turnmanager: ошибка переключения на TURN (manual): %v", err)
				return
			}
		}
	}
	m.log.Info("turnmanager: работаем через TURN вручную (retryLoop отключён)")
}

// SwitchToTURN переключает на TURN туннель.
// Вызывается watchdog при нестабильности прямого соединения.
// Безопасен для вызова из нескольких горутин — повторный вызов игнорируется.
func (m *Manager) SwitchToTURN() {
	m.mu.Lock()
	if m.mode == ModeTURN {
		m.mu.Unlock()
		return
	}
	m.mode = ModeTURN
	m.mu.Unlock()

	m.log.Info("turnmanager: переключение на TURN туннель (%s)", TURNProxyAddr)
	notification.Send("Proxy", "Обход белых списков включён")

	if m.cfg.OnSwitch != nil {
		if err := m.cfg.OnSwitch(ModeTURN); err != nil {
			m.log.Error("turnmanager: ошибка переключения на TURN: %v", err)
			return
		}
	}
	m.log.Info("turnmanager: работаем через TURN (VK DTLS 1.2)")

	// Запускаем фоновую проверку возврата на прямое соединение.
	m.startRetryLoop()
}

// SwitchToDirect переключает обратно на прямое соединение.
// Вызывается автоматически retryLoop или при восстановлении.
// Сбрасывает флаг ручного управления — после этого retryLoop возобновляется при следующем SwitchToTURN.
func (m *Manager) SwitchToDirect() {
	m.mu.Lock()
	if m.mode == ModeDirect {
		m.manual = false
		m.mu.Unlock()
		return
	}
	m.mode = ModeDirect
	m.manual = false
	m.mu.Unlock()

	m.log.Info("turnmanager: возврат на прямое соединение")
	m.stopRetryLoop()

	if m.cfg.OnSwitch != nil {
		if err := m.cfg.OnSwitch(ModeDirect); err != nil {
			m.log.Error("turnmanager: ошибка переключения на direct: %v", err)
			return
		}
	}
	notification.Send("Proxy", "Прямое соединение восстановлено ✓")
}

// startRetryLoop запускает горутину, которая каждые RetryInterval проверяет
// возможность вернуться на прямое соединение.
func (m *Manager) startRetryLoop() {
	m.mu.Lock()
	if m.cancel != nil {
		m.mu.Unlock()
		return // уже запущена
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.mu.Unlock()

	go m.retryLoop(ctx)
}

// stopRetryLoop останавливает фоновую горутину проверки.
func (m *Manager) stopRetryLoop() {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
}

// retryLoop каждые RetryInterval проверяет прямое соединение.
// При успехе — переключает обратно на direct.
func (m *Manager) retryLoop(ctx context.Context) {
	ticker := time.NewTicker(m.cfg.RetryInterval)
	defer ticker.Stop()

	m.log.Info("turnmanager: retryLoop запущен (интервал: %s)", m.cfg.RetryInterval)

	for {
		select {
		case <-ctx.Done():
			m.log.Info("turnmanager: retryLoop остановлен")
			return
		case <-ticker.C:
			m.log.Info("turnmanager: проверка прямого соединения...")
			checkCtx, cancel := context.WithTimeout(ctx, DirectCheckTimeout)
			ok := m.cfg.CheckDirect(checkCtx)
			cancel()

			if ok {
				m.log.Info("turnmanager: прямое соединение доступно — проверяем ручной режим...")
				m.mu.Lock()
				isManual := m.manual
				m.mu.Unlock()
				if isManual {
					m.log.Info("turnmanager: ручной режим активен — остаёмся на TURN")
					continue
				}
				m.log.Info("turnmanager: прямое соединение доступно — возвращаемся")
				m.SwitchToDirect()
				return
			}
			m.log.Info("turnmanager: прямое соединение недоступно — остаёмся на TURN")
		}
	}
}

// Shutdown останавливает все фоновые горутины.
func (m *Manager) Shutdown() {
	m.stopRetryLoop()
}
