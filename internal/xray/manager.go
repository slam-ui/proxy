package xray

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"proxyclient/internal/logger"
)

// Config конфигурация менеджера процесса
type Config struct {
	ExecutablePath  string
	ConfigPath      string
	SecretKeyPath   string    // путь к файлу с VLESS-ключом (secret.key)
	Args            []string  // дополнительные аргументы перед -c (например: "run")
	Logger          logger.Logger
	// SingBoxWriter если задан — stdout и stderr sing-box дополнительно пишутся в этот Writer.
	// Используется для захвата вывода в event log. Если nil — пишется только в os.Stdout/Stderr.
	SingBoxWriter   io.Writer
}

// Manager интерфейс для управления процессом
type Manager interface {
	Stop() error
	IsRunning() bool
	GetPID() int
	Wait() error
}

type manager struct {
	cmd    *exec.Cmd
	config Config
	logger logger.Logger
	mu     sync.RWMutex
	done   chan struct{} // закрывается когда процесс завершился
}

func NewManager(cfg Config) (Manager, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, fmt.Errorf("невалидная конфигурация: %w", err)
	}

	m := &manager{
		config: cfg,
		logger: cfg.Logger,
		done:   make(chan struct{}),
	}

	if err := m.start(); err != nil {
		return nil, err
	}

	return m, nil
}

func validateConfig(cfg Config) error {
	if cfg.ExecutablePath == "" {
		return fmt.Errorf("отсутствует путь к исполняемому файлу")
	}
	if cfg.ConfigPath == "" {
		return fmt.Errorf("отсутствует путь к конфигурации")
	}
	if cfg.Logger == nil {
		return fmt.Errorf("отсутствует логгер")
	}
	if _, err := os.Stat(cfg.ExecutablePath); os.IsNotExist(err) {
		return fmt.Errorf("исполняемый файл не найден: %s", cfg.ExecutablePath)
	}
	if _, err := os.Stat(cfg.ConfigPath); os.IsNotExist(err) {
		return fmt.Errorf("файл конфигурации не найден: %s", cfg.ConfigPath)
	}
	return nil
}

func (m *manager) start() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// BUG FIX: append(m.config.Args, ...) мутирует исходный слайс если cap > len.
	// Копируем в новый слайс чтобы гарантировать изоляцию.
	args := make([]string, 0, len(m.config.Args)+2)
	args = append(args, m.config.Args...)
	args = append(args, "-c", m.config.ConfigPath)

	cmd := exec.Command(m.config.ExecutablePath, args...)
	if m.config.SingBoxWriter != nil {
		// Дублируем вывод: os.Stdout/Stderr (для консоли) + SingBoxWriter (для event log)
		cmd.Stdout = io.MultiWriter(os.Stdout, m.config.SingBoxWriter)
		cmd.Stderr = io.MultiWriter(os.Stderr, m.config.SingBoxWriter)
	} else {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("не удалось запустить процесс: %w", err)
	}

	m.cmd = cmd
	m.logger.Info("XRay успешно запущен с PID: %d", cmd.Process.Pid)

	// Единственный вызов Wait — здесь в горутине-мониторе
	go m.monitor()
	return nil
}

// monitor — единственное место где вызывается cmd.Wait()
func (m *manager) monitor() {
	err := m.cmd.Wait()
	close(m.done) // сигнализируем всем ждущим
	if err != nil {
		m.logger.Warn("XRay завершился с ошибкой: %v", err)
	} else {
		m.logger.Info("Процесс XRay завершён")
	}
}

func (m *manager) Stop() error {
	m.mu.Lock()
	if m.cmd == nil || m.cmd.Process == nil {
		m.mu.Unlock()
		return fmt.Errorf("процесс не запущен")
	}
	pid := m.cmd.Process.Pid
	proc := m.cmd.Process
	m.mu.Unlock()

	m.logger.Info("Остановка процесса XRay (PID: %d)...", pid)

	// На Windows os.Interrupt (SIGINT) не поддерживается для дочерних процессов —
	// Process.Signal всегда возвращает ошибку. Вместо ложного мягкого стопа
	// ждём завершения через короткий таймаут и затем выполняем Kill.
	select {
	case <-m.done:
		m.logger.Info("Процесс XRay завершился самостоятельно")
		return nil
	case <-time.After(500 * time.Millisecond):
	}

	m.logger.Info("Принудительная остановка XRay (PID: %d)...", pid)
	if err := proc.Kill(); err != nil {
		return fmt.Errorf("не удалось остановить процесс: %w", err)
	}
	<-m.done
	m.logger.Info("Процесс XRay принудительно остановлен")
	return nil
}

// IsRunning проверяет что процесс реально ещё работает
func (m *manager) IsRunning() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.cmd == nil || m.cmd.Process == nil {
		return false
	}

	// Если канал закрыт — процесс точно завершился
	select {
	case <-m.done:
		return false
	default:
	}

	// Уточняем через Windows API: GetExitCodeProcess.
	// PROCESS_QUERY_LIMITED_INFORMATION (0x1000) работает без полных прав админа.
	// Если OpenProcess упал — доверяем каналу done: раз не закрыт, процесс жив.
	const PROCESS_QUERY_LIMITED_INFORMATION = 0x1000
	handle, err := syscall.OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(m.cmd.Process.Pid))
	if err != nil {
		return true // нет прав на хэндл, но done открыт → считаем живым
	}
	defer syscall.CloseHandle(handle)

	var exitCode uint32
	if err := syscall.GetExitCodeProcess(handle, &exitCode); err != nil {
		return true
	}
	return exitCode == 259 // STILL_ACTIVE
}

func (m *manager) GetPID() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cmd == nil || m.cmd.Process == nil {
		return 0
	}
	return m.cmd.Process.Pid
}

// Wait ждёт завершения через канал — безопасно вызывать несколько раз
func (m *manager) Wait() error {
	m.mu.RLock()
	if m.cmd == nil {
		m.mu.RUnlock()
		return fmt.Errorf("процесс не запущен")
	}
	m.mu.RUnlock()
	<-m.done
	return nil
}
