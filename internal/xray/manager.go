package xray

import (
	"fmt"
	"os"
	"os/exec"
)

// Manager управляет процессом XRay
type Manager struct {
	Cmd *exec.Cmd
}

// NewManager создаёт новый менеджер
func NewManager(xrayPath, configPath string) (*Manager, error) {
	cmd := exec.Command(xrayPath, "-c", configPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Start()
	if err != nil {
		return nil, fmt.Errorf("ошибка при запуске Xray: %w", err)
	}

	fmt.Printf("Xray запущен успешно с PID: %d\n", cmd.Process.Pid)

	return &Manager{Cmd: cmd}, nil
}

// Stop останавливает XRay
func (m *Manager) Stop() error {
	fmt.Println("Завершаем процесс Xray...")
	err := m.Cmd.Process.Kill()
	if err != nil {
		return fmt.Errorf("не удалось остановить процесс Xray: %w", err)
	}
	fmt.Println("Процесс Xray успешно остановлен.")
	return nil
}

// Wait ждёт завершения (для graceful shutdown)
func (m *Manager) Wait() {
	m.Cmd.Wait()
}
