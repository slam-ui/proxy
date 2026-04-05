package netutil

import (
	"context"
	"net"
	"testing"
	"time"
)

// ── Отменённый контекст до вызова ────────────────────────────────────────

// BUG-РИСК: контекст отменённый ДО вызова WaitForPort должен вернуть
// ошибку быстро, не делая полный DialTimeout (200мс).
func TestWaitForPort_AlreadyCancelledContext_ReturnsImmediately(t *testing.T) {
	ln, addr := startTCPListener(t)
	ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // отменяем ДО вызова

	start := time.Now()
	err := WaitForPort(ctx, addr, 10*time.Second)
	elapsed := time.Since(start)

	if err == nil {
		t.Error("WaitForPort с отменённым контекстом должен вернуть ошибку")
	}
	// Допуск 500мс: даже если первый dial выполняется до проверки ctx,
	// следующая итерация должна поймать отмену.
	if elapsed > 500*time.Millisecond {
		t.Errorf("WaitForPort завис на %v при отменённом контексте (want < 500ms)", elapsed)
	}
}

// ── Контекст отменяется во время ожидания ────────────────────────────────

// BUG-РИСК: WaitForPort должен реагировать на ctx.Cancel() во время ожидания.
func TestWaitForPort_ContextCancelledDuringWait_ExitsFast(t *testing.T) {
	ln, addr := startTCPListener(t)
	ln.Close() // порт закрыт — функция будет ждать

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- WaitForPort(ctx, addr, 30*time.Second)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Error("WaitForPort должен вернуть ошибку после отмены контекста")
		}
	case <-time.After(2 * time.Second):
		t.Error("WaitForPort не отреагировал на отмену контекста за 2с — утечка горутины?")
	}
}

// ── Невалидный адрес ──────────────────────────────────────────────────────

// BUG-РИСК: невалидный адрес (не host:port) должен вернуть ошибку, не паниковать.
func TestWaitForPort_InvalidAddress_ReturnsError(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("WaitForPort с невалидным addr вызвал panic: %v", r)
		}
	}()
	err := WaitForPort(context.Background(), "not-a-valid-addr", 100*time.Millisecond)
	if err == nil {
		t.Error("невалидный адрес должен вернуть ошибку")
	}
}

// ── Порт закрывается во время ожидания ───────────────────────────────────

// BUG-РИСК: если порт открылся, потом закрылся — WaitForPort уже подключился
// и должен был вернуть nil. Проверяем что не зависаем после первого успеха.
func TestWaitForPort_PortClosedAfterConnect_AlreadyReturned(t *testing.T) {
	ln, addr := startTCPListener(t)

	done := make(chan error, 1)
	go func() {
		done <- WaitForPort(context.Background(), addr, 2*time.Second)
	}()

	// Закрываем порт — но WaitForPort уже должен был подключиться и вернуть nil
	time.Sleep(100 * time.Millisecond)
	ln.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("WaitForPort вернул ошибку после успешного подключения: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("WaitForPort завис")
	}
}

// ── Несколько одновременных листенеров ────────────────────────────────────

// BUG-РИСК: WaitForPort не должен иметь глобального состояния —
// несколько горутин на разных портах работают независимо.
func TestWaitForPort_MultiplePortsConcurrent(t *testing.T) {
	ln1, addr1 := startTCPListener(t)
	defer ln1.Close()
	ln2, addr2 := startTCPListener(t)
	defer ln2.Close()

	type result struct {
		addr string
		err  error
	}
	ch := make(chan result, 2)

	go func() {
		err := WaitForPort(context.Background(), addr1, 2*time.Second)
		ch <- result{addr1, err}
	}()
	go func() {
		err := WaitForPort(context.Background(), addr2, 2*time.Second)
		ch <- result{addr2, err}
	}()

	for i := 0; i < 2; i++ {
		r := <-ch
		if r.err != nil {
			t.Errorf("WaitForPort(%s) = %v", r.addr, r.err)
		}
	}
}

// ── Порт открывается именно на нужном адресе ─────────────────────────────

// Проверяем что WaitForPort не путает IPv4 127.0.0.1 и [::1].
func TestWaitForPort_IPv4Loopback_Correct(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	if err := WaitForPort(context.Background(), addr, 2*time.Second); err != nil {
		t.Errorf("WaitForPort(%s) = %v", addr, err)
	}
}
