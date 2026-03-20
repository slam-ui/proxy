package xray

import (
	"bytes"
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
	SingBoxWriter io.Writer
	// FileWriter если задан — stderr sing-box дублируется сюда (обычно основной лог-файл).
	FileWriter io.Writer
	// OnCrash вызывается если sing-box завершился сам (не через Stop()).
	// Вызывается из отдельной горутины — не блокирует monitor().
	OnCrash func(err error)
	// BeforeRestart вызывается перед каждым запуском sing-box (Start/doStart).
	// Используется для wintun cleanup без импорта wintun в api пакет.
	// nil = ничего не делать (default, достаточно для тестов).
	BeforeRestart func(log logger.Logger) error
}

// Manager интерфейс для управления процессом
type Manager interface {
	Start() error
	Stop() error
	IsRunning() bool
	GetPID() int
	Wait() error
	// LastOutput возвращает последние N байт stderr sing-box.
	LastOutput() string
}

// ── tailWriter ────────────────────────────────────────────────────────────────

// tailWriter захватывает последние maxTail байт вывода (для диагностики краша).
// Увеличен до 32KB по аналогии с nekoray (max_log_line = 200 строк):
// при wintun конфликте лог содержит много строк инициализации TUN до FATAL,
// 4KB могли не захватить начало ошибки.
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
		// BUG FIX: при обрезке ищем первый перенос строки чтобы не резать строку посередине.
		// Иначе crash-детектор может не найти "Cannot create a file" если сообщение
		// оказалось на границе 32KB буфера.
		trimmed := tw.buf[len(tw.buf)-tw.max:]
		if idx := bytes.IndexByte(trimmed, '\n'); idx >= 0 {
			trimmed = trimmed[idx+1:]
		}
		tw.buf = trimmed
	}
	return len(p), nil
}

func (tw *tailWriter) String() string {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	return string(tw.buf)
}

// ── crashTracker ─────────────────────────────────────────────────────────────

// crashTracker реализует rate limiting перезапусков по аналогии с nekoray:
//
//	QElapsedTimer coreRestartTimer;
//	if (coreRestartTimer.restart() < 10 * 1000) { stop retrying }
//
// Если crashCount последовательных крашей происходит быстрее чем каждые
// minInterval, считаем что core "exits too frequently" и прекращаем авторестарт.
type crashTracker struct {
	mu          sync.Mutex
	count       int       // количество крашей в текущем окне
	lastCrashAt time.Time // время последнего краша
}

const (
	crashRateWindow = 2 * time.Minute // окно для подсчёта крашей
	maxCrashCount   = 3               // максимум крашей за окно до остановки авторестарта
)

// Record регистрирует краш. Возвращает текущий счётчик крашей в окне.
// Если с последнего краша прошло больше crashRateWindow — счётчик сбрасывается.
func (ct *crashTracker) Record() int {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	if time.Since(ct.lastCrashAt) > crashRateWindow {
		ct.count = 0
	}
	ct.count++
	ct.lastCrashAt = time.Now()
	return ct.count
}

// Reset сбрасывает счётчик (вызывается при успешном старте).
func (ct *crashTracker) Reset() {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	ct.count = 0
}

// ── manager ───────────────────────────────────────────────────────────────────

type manager struct {
	cmd        *exec.Cmd
	config     Config
	logger     logger.Logger
	mu         sync.RWMutex
	done       chan struct{} // закрывается когда процесс завершился
	stopped    bool          // true если Stop() уже был вызван
	tail       *tailWriter   // последние 32KB stderr для диагностики краша
	crashes    crashTracker  // rate limiter для авторестартов (как в nekoray)
	firstStart bool          // true для первого старта — BeforeRestart не вызывается
}

func NewManager(cfg Config) (Manager, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, fmt.Errorf("невалидная конфигурация: %w", err)
	}

	m := &manager{
		config:     cfg,
		logger:     cfg.Logger,
		done:       make(chan struct{}),
		firstStart: true, // первый старт — BeforeRestart пропускается
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
		return fmt.Errorf("файл %s существует но имеет размер 0 байт — замените его реальным sing-box.exe",
			cfg.ExecutablePath)
	}
	if _, err := os.Stat(cfg.ConfigPath); os.IsNotExist(err) {
		return fmt.Errorf("файл конфигурации не найден: %s", cfg.ConfigPath)
	}
	return nil
}

// Start запускает sing-box (можно вызвать повторно после Stop/краша).
func (m *manager) Start() error {
	m.mu.Lock()
	m.done = make(chan struct{})
	m.stopped = false
	m.mu.Unlock()
	m.crashes.Reset() // успешный старт — сбрасываем счётчик крашей
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

	// CREATE_NO_WINDOW: подавляет отдельное консольное окно для sing-box.exe
	// в билдах с -H windowsgui.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x08000000,
		HideWindow:    true,
	}

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

	cmd.Stdout = buildWriter(false)
	stderrBase := buildWriter(true)
	// 32KB tail — увеличено с 4KB, чтобы захватить полный контекст wintun ошибки
	// (по аналогии с nekoray max_log_line = 200 строк)
	tailBuf := newTailWriter(32 * 1024)
	if stderrBase == io.Discard {
		cmd.Stderr = tailBuf
	} else {
		cmd.Stderr = io.MultiWriter(stderrBase, tailBuf)
	}

	// BeforeRestart пропускается при первом старте (NewManager):
	// стартовый wintun cleanup уже выполнен в startBackground/PollUntilFree.
	// При повторных стартах (Start() после краша) — выполняется всегда.
	if m.config.BeforeRestart != nil && !m.firstStart {
		if err := m.config.BeforeRestart(m.logger); err != nil {
			return fmt.Errorf("BeforeRestart: %w", err)
		}
	}
	m.firstStart = false // сбрасываем флаг после первого старта
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("не удалось запустить процесс: %w", err)
	}

	m.cmd = cmd
	m.tail = tailBuf
	m.logger.Info("XRay успешно запущен с PID: %d", cmd.Process.Pid)

	// BUG FIX: передаём cmd и done как локальные переменные в monitor().
	// Если Start() вызывается пока старый monitor() ещё работает,
	// m.cmd и m.done перезаписываются. Без локальных копий:
	//   old monitor(): m.cmd.Wait() вернулся → close(m.done) где m.done уже НОВЫЙ канал
	//   new monitor(): process exits → close(NEW m.done) → PANIC: close of closed channel
	localDone := m.done
	go m.monitor(cmd, localDone)
	return nil
}

// monitor — единственное место где вызывается cmd.Wait().
// Принимает локальные копии cmd и done чтобы избежать data race с Start().
func (m *manager) monitor(cmd *exec.Cmd, done chan struct{}) {
	err := cmd.Wait()
	close(done)

	m.mu.RLock()
	wasStoppedIntentionally := m.stopped
	onCrash := m.config.OnCrash
	m.mu.RUnlock()

	if err != nil {
		m.logger.Warn("XRay завершился с ошибкой: %v", err)
	} else {
		m.logger.Info("Процесс XRay завершён")
	}

	if wasStoppedIntentionally || onCrash == nil {
		return
	}

	// Crash rate limiting — аналог nekoray coreRestartTimer:
	//   if (coreRestartTimer.restart() < 10 * 1000) { stop retrying }
	// Если за crashRateWindow происходит maxCrashCount или более крашей —
	// прекращаем авторестарты и уведомляем пользователя.
	count := m.crashes.Record()
	if count >= maxCrashCount {
		m.logger.Error(
			"[Error] Core exits too frequently (%d раз за %v) — авторестарт отключён",
			count, crashRateWindow)
		// Передаём краш с флагом "too many" через специальную ошибку
		go onCrash(&tooManyRestartsError{count: count, base: err})
		return
	}

	go onCrash(err)
}

// tooManyRestartsError сигнализирует что авторестарт заблокирован из-за rate limit.
// OnCrash может проверить этот тип чтобы не пытаться снова перезапустить.
type tooManyRestartsError struct {
	count int
	base  error
}

func (e *tooManyRestartsError) Error() string {
	return fmt.Sprintf("core crashes too frequently (%d times in %v): %v",
		e.count, crashRateWindow, e.base)
}

// IsTooManyRestarts возвращает true если ошибка означает блокировку rate limiter.
func IsTooManyRestarts(err error) bool {
	_, ok := err.(*tooManyRestartsError)
	return ok
}

func (m *manager) Stop() error {
	m.mu.Lock()
	if m.cmd == nil || m.cmd.Process == nil {
		m.mu.Unlock()
		return fmt.Errorf("процесс не запущен")
	}
	pid := m.cmd.Process.Pid
	proc := m.cmd.Process
	m.stopped = true
	// BUG FIX: захватываем done под lock чтобы избежать data race с Start().
	// Start() заменяет m.done под mu.Lock() — читать m.done нужно тоже под lock.
	doneCh := m.done
	m.mu.Unlock()

	m.logger.Info("Остановка процесса XRay (PID: %d)...", pid)

	select {
	case <-doneCh:
		m.logger.Info("Процесс XRay завершился самостоятельно")
		return nil
	case <-time.After(500 * time.Millisecond):
	}

	m.logger.Info("Принудительная остановка XRay (PID: %d)...", pid)
	if err := proc.Kill(); err != nil {
		return fmt.Errorf("не удалось остановить процесс: %w", err)
	}
	<-doneCh
	m.logger.Info("Процесс XRay принудительно остановлен")
	return nil
}

func (m *manager) LastOutput() string {
	if m.tail == nil {
		return ""
	}
	return m.tail.String()
}

// IsRunning проверяет что процесс реально ещё работает.
// Упрощено по сравнению с предыдущей версией: канал done уже точно отвечает
// на вопрос "завершился ли процесс". OpenProcess/GetExitCodeProcess был лишним
// Win32 вызовом который делал код непортируемым и не давал новой информации.
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
		return true
	}
}

func (m *manager) GetPID() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cmd == nil || m.cmd.Process == nil {
		return 0
	}
	return m.cmd.Process.Pid
}

// Wait ждёт завершения через канал — безопасно вызывать несколько раз.
func (m *manager) Wait() error {
	m.mu.RLock()
	if m.cmd == nil {
		m.mu.RUnlock()
		return fmt.Errorf("процесс не запущен")
	}
	// BUG FIX: захватываем done под lock.
	doneCh := m.done
	m.mu.RUnlock()
	<-doneCh
	return nil
}

// isStdoutValid reports whether os.Stdout is a usable file handle.
// In windowsgui builds (-H windowsgui) Windows does not attach a console.
func isStdoutValid() bool {
	if os.Stdout == nil {
		return false
	}
	_, err := os.Stdout.Stat()
	return err == nil
}
