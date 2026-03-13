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
	ExecutablePath string
	ConfigPath     string
	SecretKeyPath  string   // путь к файлу с VLESS-ключом (secret.key)
	Args           []string // дополнительные аргументы перед -c (например: "run")
	Logger         logger.Logger
	// SingBoxWriter если задан — stdout и stderr sing-box дополнительно пишутся в этот Writer.
	// Используется для захвата вывода в event log. Если nil — пишется только в os.Stdout/Stderr.
	SingBoxWriter io.Writer
	// FileWriter если задан — stderr sing-box дублируется сюда (обычно основной лог-файл).
	// Это позволяет видеть ошибки запуска sing-box прямо в proxy-client.log.
	FileWriter io.Writer
	// OnCrash вызывается если sing-box завершился сам (не через Stop()).
	// Используется для отключения системного прокси при неожиданном падении процесса.
	// Вызывается из отдельной горутины — не блокирует monitor().
	OnCrash func(err error)
}

// Manager интерфейс для управления процессом
type Manager interface {
	// Start запускает (или перезапускает) процесс sing-box.
	Start() error
	Stop() error
	IsRunning() bool
	GetPID() int
	Wait() error
	// LastOutput возвращает последние N байт stderr sing-box.
	LastOutput() string
}

// tailWriter захватывает последние maxTail байт вывода (для диагностики краша)
type tailWriter struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func newTailWriter(max int) *tailWriter { return &tailWriter{max: max, buf: make([]byte, 0, max)} }

func (tw *tailWriter) Write(p []byte) (int, error) {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	tw.buf = append(tw.buf, p...)
	if len(tw.buf) > tw.max {
		tw.buf = tw.buf[len(tw.buf)-tw.max:]
	}
	return len(p), nil
}

func (tw *tailWriter) String() string {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	return string(tw.buf)
}

type manager struct {
	cmd     *exec.Cmd
	config  Config
	logger  logger.Logger
	mu      sync.RWMutex
	done    chan struct{} // закрывается когда процесс завершился
	stopped bool         // true если Stop() уже был вызван (не неожиданный краш)
	tail    *tailWriter  // последние 4KB stderr для диагностики краша
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

	if err := m.doStart(); err != nil {
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
	fi, err := os.Stat(cfg.ExecutablePath)
	if os.IsNotExist(err) {
		return fmt.Errorf("исполняемый файл не найден: %s", cfg.ExecutablePath)
	}
	if err == nil && fi.Size() == 0 {
		return fmt.Errorf("файл %s существует но имеет размер 0 байт — замените его реальным sing-box.exe", cfg.ExecutablePath)
	}
	if _, err := os.Stat(cfg.ConfigPath); os.IsNotExist(err) {
		return fmt.Errorf("файл конфигурации не найден: %s", cfg.ConfigPath)
	}
	return nil
}

// Start запускает sing-box процесс (можно вызвать повторно после Stop/краша).
func (m *manager) Start() error {
	m.mu.Lock()
	m.done = make(chan struct{}) // новый канал для нового запуска
	m.stopped = false
	m.mu.Unlock()
	return m.doStart()
}

func (m *manager) doStart() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// BUG FIX: append(m.config.Args, ...) мутирует исходный слайс если cap > len.
	args := make([]string, 0, len(m.config.Args)+2)
	args = append(args, m.config.Args...)
	args = append(args, "-c", m.config.ConfigPath)

	cmd := exec.Command(m.config.ExecutablePath, args...)

	// Hide the sing-box console window.
	// When proxy-client is built with -H windowsgui, Windows would otherwise
	// create a separate console window for every child process that is itself
	// a console application (like sing-box.exe).
	// CREATE_NO_WINDOW (0x08000000) suppresses that window entirely.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
		HideWindow:    true,
	}

	// Route sing-box output:
	//   - In windowsgui builds os.Stdout/Stderr are invalid (nil handles) —
	//     writing to them would panic. We detect this by checking the file stat.
	//   - In debug/console builds we also tee to os.Stdout so output is visible
	//     in the terminal alongside the main app logs.
	// Build writers for stdout and stderr.
	// Priority: console (if valid) > SingBoxWriter (event log) > FileWriter (log file)
	// stderr always includes FileWriter so crash reasons appear in proxy-client.log.
	stdoutOK := isStdoutValid()

	buildWriter := func(includeFile bool) io.Writer {
		var writers []io.Writer
		if stdoutOK {
			writers = append(writers, os.Stdout)
		}
		if m.config.SingBoxWriter != nil {
			writers = append(writers, m.config.SingBoxWriter)
		}
		if includeFile && m.config.FileWriter != nil {
			writers = append(writers, m.config.FileWriter)
		}
		switch len(writers) {
		case 0:
			return io.Discard
		case 1:
			return writers[0]
		default:
			return io.MultiWriter(writers...)
		}
	}

	cmd.Stdout = buildWriter(false) // stdout: console + event log
	// stderr: console + event log + file log + tail buffer (для детекции ошибок краша)
	stderrBase := buildWriter(true)
	tailBuf := newTailWriter(4096)
	if stderrBase == io.Discard {
		cmd.Stderr = tailBuf
	} else {
		cmd.Stderr = io.MultiWriter(stderrBase, tailBuf)
	}
	// Сохраняем ссылку — она будет скопирована в m.tail после cmd.Start()
	_ = tailBuf

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("не удалось запустить процесс: %w", err)
	}

	m.cmd = cmd
	m.tail = tailBuf // последние 4KB stderr (уже подключён к cmd.Stderr)
	m.logger.Info("XRay успешно запущен с PID: %d", cmd.Process.Pid)

	// BUG FIX: захватываем локальную ссылку на cmd до запуска горутины.
	// monitor() не может читать m.cmd напрямую — это data race:
	// Start() может вызваться из OnCrash (отдельная горутина) и перезаписать
	// m.cmd пока старый monitor() ещё не вернулся из Wait().
	// Локальная переменная cmdRef гарантирует что каждый monitor() ждёт
	// именно свой процесс, независимо от последующих переприсвоений m.cmd.
	cmdRef := cmd
	go m.monitor(cmdRef)
	return nil
}

// monitor — единственное место где вызывается cmd.Wait()
func (m *manager) monitor(cmd *exec.Cmd) {
	err := cmd.Wait()
	close(m.done)

	m.mu.RLock()
	wasStoppedIntentionally := m.stopped
	onCrash := m.config.OnCrash
	m.mu.RUnlock()

	if err != nil {
		m.logger.Warn("XRay завершился с ошибкой: %v", err)
	} else {
		m.logger.Info("Процесс XRay завершён")
	}

	// BUG FIX: если процесс упал сам (не через Stop()), уведомляем вызывающий код.
	// До этого исправления: sing-box падал из-за невалидного конфига, системный прокси
	// оставался включён и указывал на мёртвый порт — весь трафик уходил в никуда.
	if !wasStoppedIntentionally && onCrash != nil {
		go onCrash(err)
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
	m.stopped = true // помечаем до Kill — monitor() должен увидеть флаг
	m.mu.Unlock()

	m.logger.Info("Остановка процесса XRay (PID: %d)...", pid)

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

func (m *manager) LastOutput() string {
	if m.tail == nil {
		return ""
	}
	return m.tail.String()
}

// IsRunning проверяет что процесс реально ещё работает
func (m *manager) IsRunning() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.cmd == nil || m.cmd.Process == nil {
		return false
	}

	select {
	case <-m.done:
		return false
	default:
	}

	const PROCESS_QUERY_LIMITED_INFORMATION = 0x1000
	handle, err := syscall.OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(m.cmd.Process.Pid))
	if err != nil {
		return true
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

// isStdoutValid reports whether os.Stdout is a usable file handle.
// In windowsgui builds (-H windowsgui) Windows does not attach a console,
// so os.Stdout.Stat() fails — writing to it would panic or silently corrupt.
func isStdoutValid() bool {
	if os.Stdout == nil {
		return false
	}
	_, err := os.Stdout.Stat()
	return err == nil
}
