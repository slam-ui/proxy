package netutil

import (
	"context"
	"fmt"
	"net"
	"time"
)

// WaitForPort ждёт пока TCP-порт станет доступен, или истечёт таймаут / контекст.
// Возвращает nil при успехе, ctx.Err() при отмене контекста, ошибку при таймауте.
//
// Оптимизация vs оригинал:
//   - Первая попытка сразу (без sleep): sing-box часто готов за <50мс.
//   - Экспоненциальный backoff: 10мс → 25мс → 50мс → 100мс (cap).
//     Уменьшает среднее время ожидания в 2-3x при быстром старте.
//   - DialTimeout 200мс вместо 500мс: localhost всегда отвечает мгновенно.
//   - BUG FIX #7: ctx позволяет прервать ожидание при graceful shutdown —
//     без этого горутина спала до 15с блокируя нормальное завершение.
func WaitForPort(ctx context.Context, addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	// Первая попытка без задержки — часто sing-box уже готов.
	if conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
		conn.Close()
		return nil
	}

	// Экспоненциальный backoff: быстро опрашиваем в начале,
	// замедляемся если процесс ещё грузится.
	sleep := 10 * time.Millisecond
	const maxSleep = 100 * time.Millisecond

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sleep):
		}
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		sleep *= 2
		if sleep > maxSleep {
			sleep = maxSleep
		}
	}
	return fmt.Errorf("порт %s недоступен после %v", addr, timeout)
}
