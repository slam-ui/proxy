package proxy

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

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
}

type manager struct {
	config  Config
	enabled bool
	logger  logger.Logger
	mu      sync.RWMutex
}

func NewManager(log logger.Logger) Manager {
	enabled, addr := getSystemProxyState()
	m := &manager{
		logger:  log,
		enabled: enabled,
	}
	if enabled && addr != "" {
		m.config = Config{Address: addr}
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
