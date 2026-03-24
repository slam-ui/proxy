package netutil

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

// ── BUG-РИСК #1: первая попытка без контекста ────────────────────────────
//
// WaitForPort делает первый DialTimeout БЕЗ проверки ctx.Done().
// Если ctx уже отменён до вызова — функция всё равно сделает один Dial (200мс).
// Тест проверяет что задержка не превышает 200мс + небольшой допуск.
// (Существующий тест допускает 500мс — это маскирует медленный первый dial.)
func TestWaitForPort_PreCancelledCtx_MaxOneDialDelay(t *testing.T) {
	ln, _ := startTCPListener(t)
	addr := ln.Addr().String()
	ln.Close() // порт закрыт

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // отменяем ДО вызова

	start := time.Now()
	err := WaitForPort(ctx, addr, 30*time.Second)
	elapsed := time.Since(start)

	if err == nil {
		t.Error("должна быть ошибка при отменённом контексте")
	}
	// Максимум один DialTimeout(200мс) + накладные расходы
	if elapsed > 400*time.Millisecond {
		t.Errorf("при отменённом ctx первый Dial занял %v (want ≤400ms) — ctx не проверяется перед dial", elapsed)
	}
}

// ── BUG-РИСК #2: race между портом и контекстом ──────────────────────────
//
// WaitForPort должен вернуться сразу как только ctx отменён,
// даже если select выбирает time.After раньше ctx.Done() в одной итерации.
// Тест запускает WaitForPort с коротким sleep до отмены и убеждается
// что выход происходит в пределах одного цикла backoff (≤200мс после cancel).
func TestWaitForPort_ContextCancelledDuringBackoff_FastExit(t *testing.T) {
	ln, _ := startTCPListener(t)
	addr := ln.Addr().String()
	ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)

	go func() {
		result <- WaitForPort(ctx, addr, 60*time.Second)
	}()

	// Даём одну итерацию backoff (10мс) пройти, потом отменяем
	time.Sleep(15 * time.Millisecond)
	cancel()

	cancelTime := time.Now()
	select {
	case err := <-result:
		exitDelay := time.Since(cancelTime)
		if err == nil {
			t.Error("должна быть ошибка")
		}
		// После cancel должны выйти за время не больше maxSleep (100мс) + dial (200мс)
		if exitDelay > 350*time.Millisecond {
			t.Errorf("WaitForPort вышел через %v после cancel (want ≤350ms)", exitDelay)
		}
	case <-time.After(3 * time.Second):
		t.Error("WaitForPort не среагировал на cancel за 3с")
	}
}

// ── BUG-РИСК #3: WaitForPort возвращает ctx.Err() а не generic error ────────
//
// При отмене контекста функция должна возвращать ctx.Err() (context.Canceled
// или context.DeadlineExceeded), а не fmt.Errorf("порт недоступен...").
// Без этого caller не может отличить «таймаут» от «порт не открылся».
func TestWaitForPort_CancelReturnsCtxErr(t *testing.T) {
	ln, _ := startTCPListener(t)
	addr := ln.Addr().String()
	ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- WaitForPort(ctx, addr, 30*time.Second)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	err := <-done
	if err == nil {
		t.Fatal("должна быть ошибка")
	}
	if err != context.Canceled {
		t.Errorf("WaitForPort при cancel вернул %v, want context.Canceled — caller не может отличить причину", err)
	}
}

// ── BUG-РИСК #4: context.DeadlineExceeded vs fmt.Errorf ───────────────────
//
// Если передать ctx с Deadline, а не таймаут через параметр timeout,
// WaitForPort должен вернуть context.DeadlineExceeded, а не строковую ошибку.
func TestWaitForPort_DeadlineExceededReturnsCtxErr(t *testing.T) {
	ln, _ := startTCPListener(t)
	addr := ln.Addr().String()
	ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	err := WaitForPort(ctx, addr, 30*time.Second) // ctx закончится раньше timeout
	if err == nil {
		t.Fatal("должна быть ошибка")
	}
	if err != context.DeadlineExceeded {
		t.Logf("WaitForPort при deadline вернул %v — не context.DeadlineExceeded (возможный баг для caller)", err)
	}
}

// ── BUG-РИСК #5: backoff не должен drift бесконечно ──────────────────────
//
// Если maxSleep=100мс и timeout=200мс, функция не должна "уснуть" на 100мс
// и потом ещё раз на 100мс — суммарная задержка должна быть ≤ timeout + dial + допуск.
func TestWaitForPort_Timeout_BoundedByTimeout(t *testing.T) {
	ln, _ := startTCPListener(t)
	addr := ln.Addr().String()
	ln.Close()

	timeout := 250 * time.Millisecond
	start := time.Now()
	err := WaitForPort(context.Background(), addr, timeout)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("должна быть ошибка таймаута")
	}
	// dial=200мс + backoff overhead + допуск 300мс
	maxExpected := timeout + 200*time.Millisecond + 300*time.Millisecond
	if elapsed > maxExpected {
		t.Errorf("WaitForPort занял %v при timeout=%v (maxExpected=%v) — backoff drift?",
			elapsed, timeout, maxExpected)
	}
}

// ── BUG-РИСК #6: множество горутин с одним портом — нет race condition ─────
//
// При нескольких параллельных WaitForPort на один порт — все должны
// успешно вернуть nil без data race (запускать с -race).
func TestWaitForPort_ConcurrentCallers_AllSucceed(t *testing.T) {
	ln, addr := startTCPListener(t)
	defer ln.Close()

	var wg sync.WaitGroup
	errors := make(chan error, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := WaitForPort(context.Background(), addr, 2*time.Second); err != nil {
				errors <- err
			}
		}()
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Errorf("параллельный WaitForPort вернул ошибку: %v", err)
	}
}

// ── BUG-РИСК #7: IPv6 адрес не должен вызывать panic ────────────────────────
//
// net.DialTimeout("[::1]:8080") корректен, но "[::1]" (без порта) — нет.
// Тест проверяет что невалидный IPv6 не паникует.
func TestWaitForPort_IPv6_InvalidFormat_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("WaitForPort с IPv6 адресом вызвал panic: %v", r)
		}
	}()

	cases := []string{
		"[::1]:0",         // порт 0
		"[::1]",           // нет порта
		"::1:8080",        // неправильный формат IPv6
	}
	for _, addr := range cases {
		err := WaitForPort(context.Background(), addr, 50*time.Millisecond)
		t.Logf("WaitForPort(%q): %v", addr, err)
	}
}

// ── BUG-РИСК #8: нулевой timeout — немедленный выход ─────────────────────
//
// WaitForPort(ctx, addr, 0) должен сделать максимум одну попытку
// (первый dial без sleep) и вернуть ошибку немедленно если порт закрыт.
func TestWaitForPort_ZeroTimeout_AtMostOneDial(t *testing.T) {
	ln, _ := startTCPListener(t)
	addr := ln.Addr().String()
	ln.Close()

	start := time.Now()
	err := WaitForPort(context.Background(), addr, 0)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("должна быть ошибка")
	}
	// С timeout=0 цикл не запускается — только первый dial (200мс max)
	if elapsed > 300*time.Millisecond {
		t.Errorf("WaitForPort(timeout=0) занял %v (want ≤300ms)", elapsed)
	}
}

// ── BUG-РИСК #9: порт открывается ПОСЛЕ первой попытки ─────────────────────
//
// Реалистичный сценарий: sing-box стартует за 50мс после вызова WaitForPort.
// Тест проверяет что с маленьким timeout (500мс) функция успевает поймать
// открытый порт за несколько итераций backoff.
func TestWaitForPort_PortOpensAt50ms_CaughtWithinTimeout(t *testing.T) {
	// Получаем свободный порт
	ln0, addr := startTCPListener(t)
	ln0.Close()

	go func() {
		time.Sleep(50 * time.Millisecond)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return
		}
		defer ln.Close()
		time.Sleep(time.Second) // держим открытым
	}()

	err := WaitForPort(context.Background(), addr, 500*time.Millisecond)
	if err != nil {
		t.Errorf("WaitForPort не поймал порт открытый через 50мс при timeout=500мс: %v", err)
	}
}

// ── BUG-РИСК #10: отрицательный timeout ─────────────────────────────────────
//
// WaitForPort с отрицательным timeout не должен паниковать и должен
// вернуть ошибку (либо сразу, либо после первого dial).
func TestWaitForPort_NegativeTimeout_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("WaitForPort с отрицательным timeout вызвал panic: %v", r)
		}
	}()

	ln, _ := startTCPListener(t)
	addr := ln.Addr().String()
	ln.Close()

	err := WaitForPort(context.Background(), addr, -1*time.Second)
	if err == nil {
		t.Error("WaitForPort с закрытым портом и отрицательным timeout должен вернуть ошибку")
	}
}
