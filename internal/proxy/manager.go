package proxy

import (
	"fmt"
	"strings"
	"sync"

	"proxyclient/internal/logger"
)

// Config конфигурация прокси
type Config struct {
	Address  string
	Override string
}

// Manager интерфейс для управления системным прокси
type Manager interface {
	Enable(config Config) error
	Disable() error
	IsEnabled() bool
	GetConfig() Config
}

// manager реализация Manager
type manager struct {
	config  Config
	enabled bool
	logger  logger.Logger
	mu      sync.RWMutex
}

// NewManager создаёт новый менеджер прокси
func NewManager(log logger.Logger) Manager {
	return &manager{
		logger:  log,
		enabled: false,
	}
}

// Enable включает системный прокси
func (m *manager) Enable(config Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := validateConfig(config); err != nil {
		return fmt.Errorf("невалидная конфигурация прокси: %w", err)
	}

	if err := setSystemProxy(config.Address, config.Override); err != nil {
		return fmt.Errorf("не удалось включить системный прокси: %w", err)
	}

	m.config = config
	m.enabled = true
	m.logger.Info("Системный прокси включён: %s", config.Address)

	return nil
}

// Disable отключает системный прокси
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

// IsEnabled проверяет, включен ли прокси
func (m *manager) IsEnabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.enabled
}

// GetConfig возвращает текущую конфигурацию
func (m *manager) GetConfig() Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.config
}

// validateConfig проверяет конфигурацию
func validateConfig(config Config) error {
	if strings.TrimSpace(config.Address) == "" {
		return fmt.Errorf("отсутствует адрес прокси")
	}
	return nil
}
