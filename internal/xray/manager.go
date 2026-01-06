package xray

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"proxyclient/internal/logger"
)

// Config конфигурация XRay менеджера
type Config struct {
	ExecutablePath string
	ConfigPath     string
	Logger         logger.Logger
}

// Manager интерфейс для управления XRay
type Manager interface {
	Stop() error
	IsRunning() bool
	GetPID() int
	Wait() error
}

// manager реализация Manager
type manager struct {
	cmd    *exec.Cmd
	config Config
	logger logger.Logger
	mu     sync.RWMutex
	ctx    context.Context
	cancel context.CancelFunc
}

// NewManager создаёт новый менеджер XRay
func NewManager(cfg Config) (Manager, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, fmt.Errorf("невалидная конфигурация: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	m := &manager{
		config: cfg,
		logger: cfg.Logger,
		ctx:    ctx,
		cancel: cancel,
	}

	if err := m.start(); err != nil {
		cancel()
		return nil, err
	}

	return m, nil
}

// validateConfig проверяет конфигурацию
func validateConfig(cfg Config) error {
	if cfg.ExecutablePath == "" {
		return fmt.Errorf("отсутствует путь к исполняемому файлу XRay")
	}
	if cfg.ConfigPath == "" {
		return fmt.Errorf("отсутствует путь к конфигурации")
	}
	if cfg.Logger == nil {
		return fmt.Errorf("отсутствует логгер")
	}

	// Проверяем существование файлов
	if _, err := os.Stat(cfg.ExecutablePath); os.IsNotExist(err) {
		return fmt.Errorf("исполняемый файл XRay не найден: %s", cfg.ExecutablePath)
	}
	if _, err := os.Stat(cfg.ConfigPath); os.IsNotExist(err) {
		return fmt.Errorf("файл конфигурации не найден: %s", cfg.ConfigPath)
	}

	return nil
}

// start запускает процесс XRay
func (m *manager) start() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	cmd := exec.CommandContext(m.ctx, m.config.ExecutablePath, "-c", m.config.ConfigPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("не удалось запустить XRay: %w", err)
	}

	m.cmd = cmd
	m.logger.Info("XRay успешно запущен с PID: %d", cmd.Process.Pid)

	// Запускаем горутину для мониторинга процесса
	go m.monitor()

	return nil
}

// monitor отслеживает состояние процесса
func (m *manager) monitor() {
	if err := m.cmd.Wait(); err != nil {
		m.logger.Error("Процесс XRay завершился с ошибкой: %v", err)
	} else {
		m.logger.Info("Процесс XRay завершён")
	}
}

// Stop останавливает XRay
func (m *manager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cmd == nil || m.cmd.Process == nil {
		return fmt.Errorf("процесс XRay не запущен")
	}

	m.logger.Info("Остановка процесса XRay (PID: %d)...", m.cmd.Process.Pid)

	// Сначала пытаемся мягко завершить
	m.cancel()

	// Даём процессу 5 секунд на завершение
	done := make(chan error, 1)
	go func() {
		done <- m.cmd.Wait()
	}()

	select {
	case <-time.After(5 * time.Second):
		// Если не завершился, убиваем принудительно
		m.logger.Warn("XRay не завершился за 5 секунд, принудительная остановка...")
		if err := m.cmd.Process.Kill(); err != nil {
			return fmt.Errorf("не удалось остановить процесс: %w", err)
		}
		<-done // Ждём завершения после Kill
	case err := <-done:
		if err != nil && err.Error() != "signal: killed" {
			m.logger.Warn("XRay завершился с ошибкой: %v", err)
		}
	}

	m.logger.Info("Процесс XRay успешно остановлен")
	return nil
}

// IsRunning проверяет, запущен ли процесс
func (m *manager) IsRunning() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.cmd == nil || m.cmd.Process == nil {
		return false
	}

	// На Windows используем syscall для проверки процесса
	handle, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(m.cmd.Process.Pid))
	if err != nil {
		return false
	}
	syscall.CloseHandle(handle)
	return true
}

// GetPID возвращает PID процесса
func (m *manager) GetPID() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.cmd == nil || m.cmd.Process == nil {
		return 0
	}

	return m.cmd.Process.Pid
}

// Wait ждёт завершения процесса
func (m *manager) Wait() error {
	m.mu.RLock()
	cmd := m.cmd
	m.mu.RUnlock()

	if cmd == nil {
		return fmt.Errorf("процесс не запущен")
	}

	return cmd.Wait()
}
