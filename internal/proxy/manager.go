package proxy

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"proxyclient/internal/logger"
)

// Config конфигурация прокси
type Config struct {
	Address  string
	Override string
}

type Manager interface {
	Enable(config Config) error
	Disable() error
	IsEnabled() bool
	GetConfig() Config
	// B-2: StartGuard запускает периодическую проверку состояния системного прокси.
	// Если прокси был отключён извне (antivirus, Windows Update, Teams, etc.),
	// guard восстанавливает его с сохранённой конфигурацией.
	// interval — интервал проверки (defautl 5s). Горутина запускается в фоне,
	// вызови Stop() или отмени ctx чтобы завершить guard.
	StartGuard(ctx context.Context, interval time.Duration) error
	StopGuard()
	// БАГ 14: PauseGuard приостанавливает Proxy Guard на duration — используется во время
	// doApply чтобы guard не восстанавливал прокси пока sing-box перезапускается.
	PauseGuard(d time.Duration)
	// ResumeGuard снимает паузу Proxy Guard досрочно.
	ResumeGuard()
}

type manager struct {
	config  Config
	enabled bool
	logger  logger.Logger
	mu      sync.RWMutex

	// B-2: Proxy Guard поля
	guardCtx     context.Context
	guardCancel  context.CancelFunc
	guardRunning bool // BUG FIX: sync.Once не позволяет перезапуск после StopGuard
	// BUG FIX: guardGen — монотонный счётчик поколений guard-горутины.
	// Когда StartGuard запускает новую горутину, он инкрементирует guardGen и передаёт
	// значение в замыкание. При завершении горутина проверяет совпадение с текущим
	// m.guardGen: если не совпадает — это старая горутина, m.guardRunning трогать нельзя.
	// Без этого: при двойном вызове StartGuard старая горутина после завершения ставила
	// m.guardRunning = false, хотя новая горутина уже работала → race condition.
	guardGen int
	// БАГ 14: pausedUntil — время до которого Proxy Guard приостановлен.
	// Используется во время doApply чтобы guard не восстанавливал прокси
	// пока sing-box перезапускается.
	pausedUntil time.Time
}

func NewManager(log logger.Logger) Manager {
	enabled, addr, override := getSystemProxyState()
	m := &manager{
		logger:  log,
		enabled: enabled,
	}
	if enabled && addr != "" {
		m.config = Config{Address: addr, Override: override}
	}
	return m
}

func (m *manager) Enable(config Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := validateConfig(config); err != nil {
		return fmt.Errorf("невалидная конфигурация прокси: %w", err)
	}

	if m.enabled && m.config == config {
		m.logger.Debug("Системный прокси уже включён с теми же параметрами")
		return nil
	}

	if err := setSystemProxy(config.Address, config.Override); err != nil {
		return fmt.Errorf("не удалось включить системный прокси: %w", err)
	}

	m.config = config
	m.enabled = true
	m.logger.Info("Системный прокси включён: %s", config.Address)

	return nil
}

func (m *manager) Disable() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.enabled {
		m.logger.Debug("Системный прокси уже отключён")
		return nil
	}

	if err := disableSystemProxy(); err != nil {
		return fmt.Errorf("не удалось отключить системный прокси: %w", err)
	}

	m.enabled = false
	m.logger.Info("Системный прокси отключён")

	return nil
}

func (m *manager) IsEnabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.enabled
}

func (m *manager) GetConfig() Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.config
}

// B-2: StartGuard запускает периодическую проверку состояния системного прокси.
func (m *manager) StartGuard(ctx context.Context, interval time.Duration) error {
	if interval < 1*time.Second {
		interval = 5 * time.Second
	}

	m.mu.Lock()
	oldCancel := m.guardCancel
	m.guardCtx, m.guardCancel = context.WithCancel(ctx)
	m.guardRunning = true
	m.guardGen++
	myGen := m.guardGen
	guardCtx := m.guardCtx
	m.mu.Unlock()

	if oldCancel != nil {
		oldCancel()
	}

	go func() {
		m.guardLoop(guardCtx, interval)
		m.mu.Lock()
		// Очищаем флаг только если мы ещё актуальная горутина (не вытеснены повторным StartGuard).
		if m.guardGen == myGen {
			m.guardRunning = false
		}
		m.mu.Unlock()
	}()

	return nil
}

// StopGuard останавливает proxy guard.
func (m *manager) StopGuard() {
	m.mu.Lock()
	cancel := m.guardCancel
	m.guardCancel = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// guardLoop периодически проверяет состояние системного прокси.
// Если текущее состояние не совпадает с ожидаемым — восстанавливает.
func (m *manager) guardLoop(guardCtx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-guardCtx.Done():
			m.logger.Debug("Proxy Guard остановлен")
			return
		case <-ticker.C:
			m.checkAndRestore()
		}
	}
}

// PauseGuard приостанавливает Proxy Guard на d — checkAndRestore возвращается сразу.
func (m *manager) PauseGuard(d time.Duration) {
	m.mu.Lock()
	m.pausedUntil = time.Now().Add(d)
	m.mu.Unlock()
}

// ResumeGuard снимает паузу досрочно.
func (m *manager) ResumeGuard() {
	m.mu.Lock()
	m.pausedUntil = time.Time{}
	m.mu.Unlock()
}

// checkAndRestore проверяет нужно ли восстанавливать системный прокси.
func (m *manager) checkAndRestore() {
	m.mu.Lock()
	expectedEnabled := m.enabled
	expectedConfig := m.config
	pausedUntil := m.pausedUntil
	// БАГ 14: пропускаем восстановление пока guard приостановлен (doApply).
	if !pausedUntil.IsZero() && time.Now().Before(pausedUntil) {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	// Если мы не включали прокси — ничего не восстанавливаем
	if !expectedEnabled {
		return
	}

	// Читаем текущее состояние из реестра
	systemEnabled, systemAddr, systemOverride := getSystemProxyState()

	// Проверяем: соответствует ли текущее состояние ожидаемому
	if systemEnabled && systemAddr == expectedConfig.Address && systemOverride == expectedConfig.Override {
		// Всё в порядке
		return
	}

	// Прокси был отключён или изменён извне — восстанавливаем
	m.logger.Warn("Proxy Guard: обнаружено отключение/изменение прокси (was: %v %s %q, expected: %v %q), восстанавливаю...",
		systemEnabled, systemAddr, systemOverride, expectedConfig.Address, expectedConfig.Override)

	if err := setSystemProxy(expectedConfig.Address, expectedConfig.Override); err != nil {
		m.logger.Error("Proxy Guard: не удалось восстановить прокси: %v", err)
		return
	}

	m.logger.Info("Proxy Guard: прокси восстановлен успешно")
}

// validateConfig выполняет строгую проверку адреса прокси.
func validateConfig(config Config) error {
	addr := strings.TrimSpace(config.Address)
	if addr == "" {
		return fmt.Errorf("отсутствует адрес прокси")
	}

	// ФИКС: Используем net.SplitHostPort для проверки формата host:port
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("некорректный формат (ожидается host:port): %w", err)
	}

	if host == "" {
		// net.SplitHostPort пропускает ":8080", но для системного прокси это невалидно
		return fmt.Errorf("хост не может быть пустым")
	}

	// Проверяем диапазон порта
	p, err := strconv.Atoi(portStr)
	if err != nil {
		return fmt.Errorf("порт должен быть числом")
	}
	if p <= 0 || p > 65535 {
		return fmt.Errorf("порт вне диапазона 1-65535: %d", p)
	}

	return nil
}
