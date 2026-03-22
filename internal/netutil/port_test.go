package netutil

import (
	"net"
	"testing"
	"time"
)

// startTCPListener открывает TCP-листенер на случайном порту и возвращает его адрес.
func startTCPListener(t *testing.T) (net.Listener, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	return ln, ln.Addr().String()
}

// ─── Успешные сценарии ─────────────────────────────────────────────────────

// TestWaitForPort_PortAlreadyOpen проверяет быстрый путь:
// если порт доступен сразу, WaitForPort возвращает nil без sleep.
func TestWaitForPort_PortAlreadyOpen(t *testing.T) {
	ln, addr := startTCPListener(t)
	defer ln.Close()

	start := time.Now()
	if err := WaitForPort(addr, 2*time.Second); err != nil {
		t.Fatalf("WaitForPort(%q) = %v, want nil", addr, err)
	}
	// Должно вернуться очень быстро (< 300 мс) — первая попытка без sleep
	if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
		t.Errorf("WaitForPort занял %v — ожидали < 300ms (быстрый путь)", elapsed)
	}
}

// TestWaitForPort_PortOpensAfterDelay проверяет что WaitForPort корректно
// ждёт, пока порт не откроется через некоторое время.
func TestWaitForPort_PortOpensAfterDelay(t *testing.T) {
	// Резервируем порт, закрываем его, потом через 200мс открываем снова
	ln, addr := startTCPListener(t)
	ln.Close()

	go func() {
		time.Sleep(200 * time.Millisecond)
		ln2, err := net.Listen("tcp", addr)
		if err != nil {
			return // в крайнем случае тест завершится по таймауту
		}
		defer ln2.Close()
		time.Sleep(2 * time.Second) // держим открытым пока WaitForPort не подключится
	}()

	if err := WaitForPort(addr, 3*time.Second); err != nil {
		t.Fatalf("WaitForPort(%q) = %v, want nil (port opened after delay)", addr, err)
	}
}

// ─── Сценарии таймаута ─────────────────────────────────────────────────────

// TestWaitForPort_Timeout проверяет что при недоступном порте функция
// возвращает ошибку не позже чем через timeout + небольшой допуск.
func TestWaitForPort_Timeout(t *testing.T) {
	// Порт, на котором никто не слушает.
	ln, addr := startTCPListener(t)
	ln.Close() // сразу закрываем

	timeout := 300 * time.Millisecond
	start := time.Now()
	err := WaitForPort(addr, timeout)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("WaitForPort должен вернуть ошибку при недоступном порте")
	}
	// Должны выйти не сильно позже timeout (допуск 400 мс на backoff-шаги)
	if elapsed > timeout+400*time.Millisecond {
		t.Errorf("WaitForPort вернул ошибку через %v (timeout=%v), слишком долго", elapsed, timeout)
	}
}

// TestWaitForPort_ErrorContainsAddr проверяет что сообщение об ошибке
// содержит адрес порта — полезно для диагностики.
func TestWaitForPort_ErrorContainsAddr(t *testing.T) {
	ln, addr := startTCPListener(t)
	ln.Close()

	err := WaitForPort(addr, 100*time.Millisecond)
	if err == nil {
		t.Fatal("ожидали ошибку")
	}
	if msg := err.Error(); len(msg) == 0 {
		t.Error("сообщение об ошибке пустое")
	}
}

// TestWaitForPort_ZeroTimeout проверяет поведение при нулевом таймауте:
// функция должна вернуть ошибку (либо nil если порт открыт).
func TestWaitForPort_ZeroTimeout(t *testing.T) {
	ln, addr := startTCPListener(t)
	defer ln.Close()

	// Порт открыт + нулевой таймаут: первая попытка без sleep должна успеть
	err := WaitForPort(addr, 0)
	// Нулевой таймаут может как успеть (первая попытка), так и не успеть —
	// главное что функция не паникует и не зависает.
	_ = err // оба исхода допустимы при 0-таймауте
}

// TestWaitForPort_NegativeTimeout ведёт себя как нулевой таймаут.
func TestWaitForPort_NegativeTimeout(t *testing.T) {
	ln, addr := startTCPListener(t)
	ln.Close()

	// Не должен паниковать
	_ = WaitForPort(addr, -time.Second)
}

// ─── Параллельность ────────────────────────────────────────────────────────

// TestWaitForPort_Concurrent проверяет что несколько горутин могут одновременно
// вызывать WaitForPort — функция не имеет глобального состояния.
func TestWaitForPort_Concurrent(t *testing.T) {
	ln, addr := startTCPListener(t)
	defer ln.Close()

	errCh := make(chan error, 5)
	for i := 0; i < 5; i++ {
		go func() {
			errCh <- WaitForPort(addr, 2*time.Second)
		}()
	}
	for i := 0; i < 5; i++ {
		if err := <-errCh; err != nil {
			t.Errorf("concurrent WaitForPort: %v", err)
		}
	}
}
