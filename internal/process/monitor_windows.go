package process

import (
	"fmt"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"proxyclient/internal/apprules"
	"proxyclient/internal/logger"
)

// Monitor интерфейс для мониторинга процессов
type Monitor interface {
	Start() error
	Stop() error
	GetProcesses() []apprules.ProcessInfo
	GetProcess(pid int) (*apprules.ProcessInfo, error)
	Refresh() error
}

// monitor реализация Monitor
type monitor struct {
	processes map[int]*apprules.ProcessInfo
	logger    logger.Logger
	mu        sync.RWMutex
	ticker    *time.Ticker
	stopChan  chan struct{}
	running   bool
}

// NewMonitor создает новый process monitor
func NewMonitor(log logger.Logger) Monitor {
	return &monitor{
		processes: make(map[int]*apprules.ProcessInfo),
		logger:    log,
		stopChan:  make(chan struct{}),
	}
}

// Start запускает мониторинг процессов
func (m *monitor) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.running {
		return fmt.Errorf("monitor already running")
	}

	// Первичное сканирование
	if err := m.refresh(); err != nil {
		return fmt.Errorf("initial scan failed: %w", err)
	}

	// BUG FIX: stopChan после Stop() закрыт навсегда — пересоздаём перед запуском,
	// иначе повторный Start() приведёт к панике при чтении закрытого канала.
	m.stopChan = make(chan struct{})

	// Запускаем периодическое сканирование
	// 10с вместо 5с: CreateToolhelp32Snapshot перечисляет ВСЕ процессы системы.
	// На Windows это ~200-500 процессов, каждый с OpenProcess + GetProcessTimes.
	// Список процессов пользователя меняется редко — 10с достаточно.
	m.ticker = time.NewTicker(10 * time.Second)
	m.running = true

	go m.monitorLoop()

	m.logger.Info("Process monitor started")
	return nil
}

// Stop останавливает мониторинг
func (m *monitor) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.running {
		return nil
	}

	m.running = false
	m.ticker.Stop()
	close(m.stopChan)

	m.logger.Info("Process monitor stopped")
	return nil
}

// GetProcesses возвращает список всех процессов
func (m *monitor) GetProcesses() []apprules.ProcessInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	processes := make([]apprules.ProcessInfo, 0, len(m.processes))
	for _, proc := range m.processes {
		processes = append(processes, *proc)
	}

	return processes
}

// GetProcess возвращает информацию о процессе
func (m *monitor) GetProcess(pid int) (*apprules.ProcessInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	proc, exists := m.processes[pid]
	if !exists {
		return nil, fmt.Errorf("process %d not found", pid)
	}

	// Возвращаем копию
	procCopy := *proc
	return &procCopy, nil
}

// Refresh обновляет список процессов
func (m *monitor) Refresh() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.refresh()
}

// refresh внутренний метод для обновления (без lock)
func (m *monitor) refresh() error {
	snapshot, err := createToolhelp32Snapshot()
	if err != nil {
		return fmt.Errorf("failed to create snapshot: %w", err)
	}
	defer syscall.CloseHandle(snapshot)

	newProcesses := make(map[int]*apprules.ProcessInfo)

	snaps, err := enumProcesses(snapshot)
	if err != nil {
		return fmt.Errorf("failed to enumerate processes: %w", err)
	}

	for _, snap := range snaps {
		info, err := getProcessInfo(snap)
		if err != nil {
			continue
		}
		newProcesses[snap.PID] = info
	}

	// Обновляем кэш
	m.processes = newProcesses

	return nil
}

// monitorLoop основной цикл мониторинга
func (m *monitor) monitorLoop() {
	for {
		select {
		case <-m.ticker.C:
			m.mu.Lock()
			if err := m.refresh(); err != nil {
				m.logger.Warn("Failed to refresh processes: %v", err)
			}
			m.mu.Unlock()

		case <-m.stopChan:
			return
		}
	}
}

// Windows API functions and structures

var (
	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	procCreateToolhelp32 = kernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32First   = kernel32.NewProc("Process32FirstW")
	procProcess32Next    = kernel32.NewProc("Process32NextW")
	procOpenProcess      = kernel32.NewProc("OpenProcess")
	procQueryFullPath    = kernel32.NewProc("QueryFullProcessImageNameW")
	procGetProcessTimes  = kernel32.NewProc("GetProcessTimes")
)

const (
	TH32CS_SNAPPROCESS              = 0x00000002
	PROCESS_QUERY_INFO              = 0x0400
	PROCESS_QUERY_LIMITED_INFO      = 0x1000
	MAX_PATH                        = 260
)

type processEntry32 struct {
	Size            uint32
	CntUsage        uint32
	ProcessID       uint32
	DefaultHeapID   uintptr
	ModuleID        uint32
	CntThreads      uint32
	ParentProcessID uint32
	PriClassBase    int32
	Flags           uint32
	ExeFile         [MAX_PATH]uint16
}

// createToolhelp32Snapshot создает снимок процессов
func createToolhelp32Snapshot() (syscall.Handle, error) {
	ret, _, err := procCreateToolhelp32.Call(
		uintptr(TH32CS_SNAPPROCESS),
		0,
	)

	if ret == uintptr(syscall.InvalidHandle) {
		return syscall.InvalidHandle, err
	}

	return syscall.Handle(ret), nil
}

// processSnapshot хранит данные из снапшота
type processSnapshot struct {
	PID       int
	ParentPID int
}

// enumProcesses перечисляет все процессы, возвращает PID + ParentPID
func enumProcesses(snapshot syscall.Handle) ([]processSnapshot, error) {
	var pe processEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))

	var result []processSnapshot

	ret, _, _ := procProcess32First.Call(
		uintptr(snapshot),
		uintptr(unsafe.Pointer(&pe)),
	)

	if ret == 0 {
		return nil, fmt.Errorf("Process32First failed")
	}

	result = append(result, processSnapshot{
		PID:       int(pe.ProcessID),
		ParentPID: int(pe.ParentProcessID),
	})

	for {
		ret, _, _ := procProcess32Next.Call(
			uintptr(snapshot),
			uintptr(unsafe.Pointer(&pe)),
		)

		if ret == 0 {
			break
		}

		result = append(result, processSnapshot{
			PID:       int(pe.ProcessID),
			ParentPID: int(pe.ParentProcessID),
		})
	}

	return result, nil
}

// getProcessInfo получает информацию о процессе
func getProcessInfo(snap processSnapshot) (*apprules.ProcessInfo, error) {
	pid := snap.PID

	// Открываем процесс — запрашиваем минимально необходимые права.
	// PROCESS_VM_READ не нужен: мы не читаем память процесса.
	// Используем PROCESS_QUERY_LIMITED_INFO как fallback, если PROCESS_QUERY_INFO
	// заблокирован (защищённые процессы, антивирус и т.д.).
	handle, _, _ := procOpenProcess.Call(
		uintptr(PROCESS_QUERY_LIMITED_INFO),
		0,
		uintptr(pid),
	)
	if handle == 0 {
		// Пробуем с полными правами на чтение информации
		var err error
		handle, _, err = procOpenProcess.Call(
			uintptr(PROCESS_QUERY_INFO),
			0,
			uintptr(pid),
		)
		if handle == 0 {
			return nil, fmt.Errorf("failed to open process %d: %v", pid, err)
		}
	}
	defer syscall.CloseHandle(syscall.Handle(handle))

	// Получаем путь к executable
	var pathBuf [MAX_PATH]uint16
	size := uint32(MAX_PATH)

	ret, _, _ := procQueryFullPath.Call(
		handle,
		0,
		uintptr(unsafe.Pointer(&pathBuf[0])),
		uintptr(unsafe.Pointer(&size)),
	)

	var exePath string
	if ret != 0 {
		exePath = syscall.UTF16ToString(pathBuf[:size])
	}

	name := filepath.Base(exePath)
	if name == "." || name == "" {
		name = fmt.Sprintf("process_%d", pid)
	}

	// Получаем реальное время старта процесса через GetProcessTimes
	startTime := getProcessStartTime(syscall.Handle(handle))

	return &apprules.ProcessInfo{
		PID:         pid,
		ParentPID:   snap.ParentPID,
		Name:        name,
		Executable:  exePath,
		StartTime:   startTime,
		ProxyStatus: "UNKNOWN",
	}, nil
}

// getProcessStartTime возвращает реальное время старта процесса через GetProcessTimes.
// При ошибке возвращает нулевое время.
func getProcessStartTime(handle syscall.Handle) time.Time {
	var creationTime, exitTime, kernelTime, userTime syscall.Filetime
	ret, _, _ := procGetProcessTimes.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(&creationTime)),
		uintptr(unsafe.Pointer(&exitTime)),
		uintptr(unsafe.Pointer(&kernelTime)),
		uintptr(unsafe.Pointer(&userTime)),
	)
	if ret == 0 {
		return time.Time{}
	}
	return time.Unix(0, creationTime.Nanoseconds())
}
