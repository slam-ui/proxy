package xray

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"proxyclient/internal/logger"
)

// kernel32 для CTRL_BREAK graceful shutdown.
// Используем LazyDLL — загрузка при первом вызове, не при старте приложения.
var (
	kernel32          = syscall.NewLazyDLL("kernel32.dll")
	procGenCtrlEvt    = kernel32.NewProc("GenerateConsoleCtrlEvent")
	procAttachConsole = kernel32.NewProc("AttachConsole")
	procFreeConsole   = kernel32.NewProc("FreeConsole")
)

// sendCtrlBreak посылает CTRL_BREAK в процесс pid.
// Sing-box перехватывает это событие и выполняет graceful shutdown:
// сохраняет DNS-кэш, закрывает TUN-адаптер — что снижает вероятность
// "file already exists" при следующем старте.
func sendCtrlBreak(pid int) error {
	procAttachConsole.Call(uintptr(pid))
	defer procFreeConsole.Call()
	ret, _, err := procGenCtrlEvt.Call(
		syscall.CTRL_BREAK_EVENT,
		uintptr(pid),
	)
	if ret == 0 {
		return fmt.Errorf("GenerateConsoleCtrlEvent failed: %w", err)
	}
	return nil
}

// isAccessViolation проверяет содержит ли ошибка признаки 0xc0000005 (ACCESS_VIOLATION).
// Windows Defender и другие AV блокируют свежескачанный .exe при первом запуске —
// sing-box check возвращает exit status 0xc0000005 пока идёт облачная проверка SmartScreen.
func isAccessViolation(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "0xc0000005") || strings.Contains(s, "Access is denied")
}

// IsAccessViolation reports Windows ACCESS_VIOLATION/AV-scan startup failures.
func IsAccessViolation(err error) bool {
	return isAccessViolation(err)
}

// ValidateSingBoxConfig валидирует конфиг sing-box используя команду check.
// Запускает sing-box check -c конфигPath с таймаутом 10s на каждую попытку.
// При ACCESS_VIOLATION (0xc0000005) — повторяет до 5 раз с задержкой 3с:
// Windows Defender/SmartScreen сканирует свежескачанный .exe при первом запуске.
// Возвращает nil если конфиг валиден, иначе ошибку с output.
// B-1: Используется перед остановкой текущего процесса чтобы не потерять
// рабочую конфигурацию в случае невалидного нового конфига.
func ValidateSingBoxConfig(ctx context.Context, execPath, configPath string) error {
	// Проверяем что исполняемый файл существует
	if _, err := os.Stat(execPath); err != nil {
		return fmt.Errorf("sing-box не найден: %w", err)
	}

	// Проверяем что конфиг существует
	if _, err := os.Stat(configPath); err != nil {
		return fmt.Errorf("конфиг не найден: %w", err)
	}

	const maxAttempts = 5
	const retryDelay = 3 * time.Second

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		cmd := exec.CommandContext(checkCtx, execPath, "check", "-c", configPath)
		output, err := cmd.CombinedOutput()
		cancel()

		if err == nil {
			return nil
		}

		lastErr = fmt.Errorf("конфиг невалиден: %w\nВывод: %s", err, string(output))

		// Если это не ACCESS_VIOLATION — конфиг реально невалиден, не ретраим
		if !isAccessViolation(err) {
			return lastErr
		}

		// ACCESS_VIOLATION — AV сканирует бинарник, ретраим
		if attempt < maxAttempts {
			select {
			case <-time.After(retryDelay):
			case <-ctx.Done():
				return fmt.Errorf("валидация прервана: %w (последняя ошибка: %v)", ctx.Err(), lastErr)
			}
		}
	}
	return fmt.Errorf("валидация провалена после %d попыток (AV блокирует sing-box.exe): %w", maxAttempts, lastErr)
}

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
	// Передаёт crashedManager чтобы handleCrash мог проверить актуальность.
	OnCrash func(err error, crashedManager Manager)
	// OnGracefulStop вызывается после успешного graceful shutdown (CTRL_BREAK → Wait).
	// Используется для записи маркера CleanShutdownFile — гарантирует что следующий
	// старт не будет ждать gap/settle (wintun корректно освобождён через WintunCloseAdapter).
	// nil = ничего не делать.
	OnGracefulStop func()
	// OnSlowTun вызывается при обнаружении предупреждения "open interface take too much time to finish!"
	// в stderr sing-box. Позволяет превентивно увеличить adaptive gap до краша.
	OnSlowTun func()
	// BeforeRestart вызывается перед каждым запуском sing-box (Start/doStart).
	// Используется для wintun cleanup без импорта wintun в api пакет.
	// ctx позволяет прервать ожидание при выходе из приложения.
	// nil = ничего не делать (default, достаточно для тестов).
	BeforeRestart func(ctx context.Context, log logger.Logger) error
}

// Manager интерфейс для управления процессом
type Manager interface {
	Start() error
	// StartAfterManualCleanup запускает sing-box пропуская BeforeRestart.
	// Используется когда вызывающая сторона уже выполнила wintun cleanup сама
	// (например handleCrash) — избегает двойного PollUntilFree.
	StartAfterManualCleanup() error
	Stop() error
	IsRunning() bool
	GetPID() int
	Wait() error
	// LastOutput возвращает последние N байт stderr sing-box.
	LastOutput() string
	// Uptime возвращает время работы процесса. Ноль если не запущен.
	Uptime() time.Duration
	// GetHealthStatus возвращает текущее состояние здоровья сервиса VLESS.
	// БАГ #3: детектирует долгие периоды недоступности.
	GetHealthStatus() (errorCount int, errorRatePct float64, wouldAlert bool)
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

func newTailWriter(max int) *tailWriter {
	// BUG FIX (фаззер): make([]byte, 0, max) выделяет max байт при СОЗДАНИИ объекта.
	// При max=1MB и миллионах вызовов фаззера GC не успевает → OOM → exit status 2.
	// Используем ленивое выделение: стартовая ёмкость ограничена 4KB,
	// реальный рост происходит только при записи через Write.
	const initCap = 4 * 1024
	cap := initCap
	if max > 0 && max < cap {
		cap = max
	}
	return &tailWriter{max: max, buf: make([]byte, 0, cap)}
}

// Reset очищает буфер без освобождения памяти — переиспользуется при рестарте.
// OPT #8: ранее при каждом doStart() создавался новый tailWriter(32KB),
// старый уходил в GC. Reset() позволяет переиспользовать один буфер на весь
// lifecycle менеджера.
func (tw *tailWriter) Reset() {
	tw.mu.Lock()
	tw.buf = tw.buf[:0]
	tw.mu.Unlock()
}

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
		// BUG FIX #3: копируем в новый срез чтобы освободить старый backing array.
		// Без copy() tw.buf держит ссылку на весь предыдущий массив (32KB+),
		// который GC не может освободить — накапливается утечка памяти.
		newBuf := make([]byte, len(trimmed))
		copy(newBuf, trimmed)
		tw.buf = newBuf
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
	crashRateWindow = 10 * time.Minute // окно для подсчёта крашей
	// maxCrashCount увеличен: wintun-цикл занимает 1-5 мин, при 3 крашах за 2 мин
	// авторестарт отключался после первого же wintun-конфликта.
	// Теперь даём 10 попыток за 10 минут — этого хватает на несколько wintun-циклов.
	maxCrashCount = 10 // максимум крашей за окно до остановки авторестарта
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
	cmd     *exec.Cmd
	config  Config
	logger  logger.Logger
	mu      sync.RWMutex
	done    chan struct{}
	stopped bool
	// OPT #8: tail создаётся один раз и сбрасывается через Reset() при каждом рестарте.
	// Ранее newTailWriter(32KB) вызывался при каждом doStart() — 32KB аллокация + GC.
	tail         *tailWriter
	crashes      crashTracker
	firstStart   bool
	lifecycleCtx context.Context
	startedAt    time.Time
	// БАГ #3: healthChecker отслеживает ошибки соединений и детектирует
	// долгие периоды недоступности VLESS-сервера (>9 минут).
	healthChecker *HealthChecker
}

func NewManager(cfg Config, lifecycleCtx context.Context) (Manager, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, fmt.Errorf("невалидная конфигурация: %w", err)
	}
	if lifecycleCtx == nil {
		lifecycleCtx = context.Background()
	}

	m := &manager{
		config:        cfg,
		logger:        cfg.Logger,
		done:          make(chan struct{}),
		firstStart:    true,
		lifecycleCtx:  lifecycleCtx,
		tail:          newTailWriter(32 * 1024), // создаём один раз, сбрасываем при рестарте
		healthChecker: NewHealthChecker(),       // БАГ #3: инициализируем здоровье-чекер
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
	// FIX 8: сбрасываем счётчик крашей только при успешном старте.
	// Ранее Reset() вызывался до doStart() — если doStart падал,
	// счётчик оказывался сброшен, и цикл крашей не обнаруживался.
	if err := m.doStart(); err != nil {
		return err
	}
	m.crashes.Reset()
	return nil
}

// StartAfterManualCleanup запускает sing-box без вызова BeforeRestart.
// Вызывать когда cleanup уже выполнен внешним кодом (handleCrash).
func (m *manager) StartAfterManualCleanup() error {
	m.mu.Lock()
	m.done = make(chan struct{})
	m.stopped = false
	// BUG FIX #5: firstStart=true пропускает BeforeRestart в doStart.
	// Убрана логика с prevFirst: она создавала иллюзию отката, но между
	// Unlock() и doStart() другая горутина могла изменить firstStart — race.
	// Если doStart упал, firstStart остаётся true — корректно: вызывающий
	// (handleCrash) уже сделал cleanup вручную для этой попытки.
	m.firstStart = true
	m.mu.Unlock()
	// FIX 9: аналогично Start() — Reset только при успехе.
	if err := m.doStart(); err != nil {
		return err
	}
	m.crashes.Reset()
	return nil
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
	// OPT #8: сбрасываем существующий tailWriter вместо создания нового.
	// Reset() очищает срез без освобождения памяти — 0 аллокаций.
	m.tail.Reset()

	// БАГ #3: оборачиваем stderr с health tracking для детектирования недоступности VLESS
	stderrWithHealth := io.Writer(nil)
	if m.healthChecker != nil {
		stderrWithHealth = NewHealthTrackingWriter(stderrBase, m.healthChecker)
	} else {
		stderrWithHealth = stderrBase
	}

	// BUG FIX: проверяем stderrBase (не stderrWithHealth) чтобы определить нужен ли MultiWriter.
	// stderrWithHealth всегда *healthTrackingWriter (не io.Discard), поэтому старое условие
	// stderrWithHealth == io.Discard было всегда false — оптимизация никогда не срабатывала.
	// Когда stderrBase = io.Discard (нет логовых назначений), достаточно m.tail:
	// health-tracking поверх Discard добавлял накладные расходы без пользы.
	if stderrBase == io.Discard {
		cmd.Stderr = m.tail
	} else {
		cmd.Stderr = io.MultiWriter(stderrWithHealth, m.tail)
	}

	// БАГ-1: перехватываем "open interface take too much time" до FATAL.
	// Это предупреждение появляется за ~5с до краша — даёт время увеличить adaptive gap.
	if m.config.OnSlowTun != nil {
		cmd.Stderr = &slowTunDetector{
			dst:       cmd.Stderr,
			onSlowTun: m.config.OnSlowTun,
		}
	}

	// BeforeRestart пропускается при первом старте (NewManager):
	// стартовый wintun cleanup уже выполнен в startBackground/PollUntilFree.
	// При повторных стартах (Start() после краша) — выполняется всегда.
	if m.config.BeforeRestart != nil && !m.firstStart {
		if err := m.config.BeforeRestart(m.lifecycleCtx, m.logger); err != nil {
			return fmt.Errorf("BeforeRestart: %w", err)
		}
	}
	m.firstStart = false // сбрасываем флаг после первого старта
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("не удалось запустить процесс: %w", err)
	}

	m.cmd = cmd
	m.startedAt = time.Now()
	m.logger.Info("sing-box успешно запущен с PID: %d", cmd.Process.Pid)

	// BUG FIX: передаём cmd и done как локальные переменные в monitor().
	// Если Start() вызывается пока старый monitor() ещё работает,
	// m.cmd и m.done перезаписываются. Без локальных копий:
	//   old monitor(): m.cmd.Wait() вернулся → close(m.done) где m.done уже НОВЫЙ канал
	//   new monitor(): process exits → close(NEW m.done) → PANIC: close of closed channel
	localDone := m.done
	go m.monitor(cmd, localDone)

	// ВЫС-2: stability timer — после 3 минут без крашей считаем core "стабильным"
	// и сбрасываем счётчик крашей. Источник: singbox-launcher v0.8+, nekoray.
	const stabilityWindow = 3 * time.Minute
	go func() {
		t := time.NewTimer(stabilityWindow)
		defer t.Stop()
		select {
		case <-localDone:
			// процесс упал до истечения таймера — не сбрасываем счётчик
			return
		case <-t.C:
			m.crashes.Reset()
			m.logger.Info("sing-box стабилен %v — счётчик крашей сброшен", stabilityWindow)
		case <-m.lifecycleCtx.Done():
			return
		}
	}()

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
		if wasStoppedIntentionally {
			// Мы сами завершили процесс — exit status 1 при TerminateProcess ожидаем.
			// Логируем INFO чтобы не генерировать ложные WARN_BURST аномалии.
			m.logger.Info("Процесс sing-box принудительно остановлен (exit: %v)", err)
		} else {
			m.logger.Warn("sing-box завершился с ошибкой: %v", err)
		}
	} else {
		m.logger.Info("Процесс sing-box завершён")
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
		go onCrash(&tooManyRestartsError{count: count, base: err}, m)
		return
	}

	// OPT #10: экспоненциальный backoff перед перезапуском.
	// Без задержки при системной ошибке (кончилось место на диске, плохой конфиг)
	// получается busy-loop: 3 краша за 2 минуты → авторестарт отключается.
	// С backoff: 1й краш ждёт 1с, 2й — 2с, 3й — 4с (но не более 30с).
	// Аналог: singbox-launcher и V2RayN используют аналогичный backoff.
	backoff := time.Duration(1<<uint(count-1)) * time.Second // 1s, 2s, 4s, 8s...
	if backoff > 30*time.Second {
		backoff = 30 * time.Second
	}
	m.logger.Info("Краш #%d — перезапуск через %v...", count, backoff)
	go func() {
		select {
		case <-time.After(backoff):
		case <-m.lifecycleCtx.Done():
			// Приложение завершается — не запускаем OnCrash после Shutdown.
			return
		}
		onCrash(err, m)
	}()
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
	// FIX Bug6: НЕ устанавливаем m.stopped здесь — иначе monitor() может прочитать
	// stopped=true при реальном краше который произошёл ОДНОВРЕМЕННО с вызовом Stop().
	// Результат: OnCrash не вызывается, sing-box не перезапускается.
	doneCh := m.done
	m.mu.Unlock()

	m.logger.Info("Остановка процесса sing-box (PID: %d)...", pid)

	// Проверяем — может, процесс уже завершился сам (краш до нашего вызова).
	// В этом случае НЕ помечаем stopped=true: это был краш, а не намеренная остановка.
	select {
	case <-doneCh:
		m.logger.Info("Процесс sing-box уже завершился")
		return nil
	case <-time.After(500 * time.Millisecond):
	}

	// Процесс ещё жив — фиксируем намерение остановить ДО отправки сигнала.
	// Теперь monitor() увидит stopped=true и не вызовет OnCrash.
	m.mu.Lock()
	m.stopped = true
	m.mu.Unlock()

	// OPT #1: сначала CTRL_BREAK — sing-box перехватывает его и делает graceful shutdown:
	// сохраняет DNS-кэш, закрывает TUN-адаптер корректно.
	// Это снижает вероятность "file already exists" при следующем старте,
	// потому что wintun успевает освободить kernel-объект до Kill().
	// Аналог: singbox-launcher использует CTRL_BREAK + 3s таймаут перед taskkill /F.
	if err := sendCtrlBreak(pid); err == nil {
		m.logger.Info("Отправлен CTRL_BREAK (PID: %d), ожидаем graceful shutdown...", pid)
		select {
		case <-doneCh:
			m.logger.Info("Процесс sing-box корректно завершился (graceful)")
			// ОПТИМИЗАЦИЯ: уведомляем wintun о чистом завершении.
			// Следующий PollUntilFree пропустит все задержки (gap=0, settle=0).
			if m.config.OnGracefulStop != nil {
				m.config.OnGracefulStop()
			}
			return nil
		case <-time.After(3 * time.Second):
			m.logger.Info("Graceful shutdown timeout — принудительная остановка")
		}
	}

	m.logger.Info("Принудительная остановка sing-box (PID: %d)...", pid)
	if err := proc.Kill(); err != nil {
		return fmt.Errorf("не удалось остановить процесс: %w", err)
	}
	<-doneCh
	m.logger.Info("Процесс sing-box принудительно остановлен")
	return nil
}

func (m *manager) Uptime() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.startedAt.IsZero() {
		return 0
	}
	return time.Since(m.startedAt)
}

func (m *manager) LastOutput() string {
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

// GetHealthStatus возвращает текущее состояние здоровья сервиса VLESS.
// Вызывается из Web API для уведомления пользователя о проблемах.
// БАГ #3: детектирует долгие периоды недоступности (>X% вошибок за N сек).
func (m *manager) GetHealthStatus() (errorCount int, errorRatePct float64, wouldAlert bool) {
	if m.healthChecker == nil {
		return 0, 0, false
	}
	return m.healthChecker.GetStatus()
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

// slowTunDetector — io.Writer обёртка которая вызывает onSlowTun при обнаружении
// предупреждения "open interface take too much time to finish!" в stderr sing-box.
type slowTunDetector struct {
	dst       io.Writer
	onSlowTun func()
	once      sync.Once
}

func (s *slowTunDetector) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "open interface take too much time") {
		s.once.Do(func() { go s.onSlowTun() })
	}
	return s.dst.Write(p)
}

// TunConflictSignatures — набор строк в выводе sing-box, указывающих на конфликт
// wintun kernel-объекта. Вынесены как единственный источник истины: если sing-box
// изменит формат сообщений, достаточно обновить этот список в одном месте.
//
// Признаки конфликта:
//   - "Cannot create a file when that file already exists" — CreateAdapter падает,
//     т.к. kernel-объект \\Device\\WINTUN-{GUID} ещё занят предыдущей сессией.
//   - "configure tun interface" — обёртка sing-box вокруг той же ошибки.
//   - "A device attached to the system is not functioning" — ERROR_GEN_FAILURE (error 31),
//     stale wintun driver registration после Windows update; лечится через sc delete wintun.
var TunConflictSignatures = []string{
	"Cannot create a file when that file already exists",
	"configure tun interface",
	"A device attached to the system is not functioning",
}

// IsTunConflict проверяет, содержит ли вывод sing-box признаки wintun-конфликта.
func IsTunConflict(output string) bool {
	for _, sig := range TunConflictSignatures {
		if strings.Contains(output, sig) {
			return true
		}
	}
	return false
}

// IsCacheFileTimeout проверяет, содержит ли вывод sing-box ошибку таймаута cache-file.
// Происходит когда предыдущий экземпляр sing-box оставил заблокированный dns_cache.db.
func IsCacheFileTimeout(output string) bool {
	return strings.Contains(output, "initialize cache-file: timeout")
}
