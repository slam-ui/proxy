package netutil

import (
	"fmt"
	"net"
	"time"
)

// WaitForPort ждёт пока TCP-порт станет доступен, или истечёт таймаут.
// Возвращает nil при успехе, ошибку при таймауте.
func WaitForPort(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("порт %s недоступен после %v", addr, timeout)
}
