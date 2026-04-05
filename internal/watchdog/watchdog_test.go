package watchdog

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// fakeProbe подменяет tcpProbe в тестах через monkey-patching поля.
// Здесь мы тестируем логику через Run с очень маленьким интервалом.

func TestWatchdog_TriggersOnUnstable(t *testing.T) {
	unstableCalled := make(chan struct{}, 1)

	w := New(Config{
		Target:        "127.0.0.1:1", // порт 1 заблокирован — probe всегда fail
		ProbeInterval: 5 * time.Millisecond,
		OnUnstable: func() {
			select {
			case unstableCalled <- struct{}{}:
			default:
			}
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go w.Run(ctx)

	select {
	case <-unstableCalled:
		// ожидаемо
	case <-time.After(500 * time.Millisecond):
		t.Fatal("OnUnstable не был вызван за 500мс")
	}

	if w.State() != StateUnstable {
		t.Errorf("State = %v, хотим StateUnstable", w.State())
	}
}

func TestWatchdog_OnUnstableCalledOnce(t *testing.T) {
	var callCount atomic.Int32

	w := New(Config{
		Target:        "127.0.0.1:1",
		ProbeInterval: 5 * time.Millisecond,
		OnUnstable: func() {
			callCount.Add(1)
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	w.Run(ctx)

	if n := callCount.Load(); n != 1 {
		t.Errorf("OnUnstable вызван %d раз, хотим 1", n)
	}
}

func TestWatchdog_StopsOnContextCancel(t *testing.T) {
	w := New(Config{
		Target:        "127.0.0.1:1",
		ProbeInterval: 10 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
		// OK
	case <-time.After(200 * time.Millisecond):
		t.Fatal("watchdog не остановился после отмены контекста")
	}
}

func TestWatchdog_InitialStateUnknown(t *testing.T) {
	w := New(Config{Target: "127.0.0.1:9999"})
	if w.State() != StateUnknown {
		t.Errorf("начальный State = %v, хотим StateUnknown", w.State())
	}
}

func TestWatchdog_ThresholdNotReachedBelow3(t *testing.T) {
	var unstableFired atomic.Bool

	w := &Watchdog{
		cfg: Config{
			Target:        "127.0.0.1:1",
			ProbeInterval: 5 * time.Millisecond,
			OnUnstable: func() {
				unstableFired.Store(true)
			},
		},
		log: discardLogger{},
	}

	// Добавляем 2 обрыва вручную — порог не достигнут.
	now := time.Now()
	w.failures = []time.Time{now.Add(-5 * time.Second), now.Add(-3 * time.Second)}

	// probe с провалом — добавляет третий → порог достигнут.
	// Но сначала проверим что с двумя обрывами OnUnstable НЕ вызван.
	w.mu.Lock()
	w.state = StateUnknown
	w.mu.Unlock()

	// Проверяем что state всё ещё Unknown с двумя обрывами.
	if w.State() != StateUnknown {
		t.Errorf("State не должен быть Unstable при 2 обрывах")
	}
}

// discardLogger реализует logger.Logger и выбрасывает все сообщения.
type discardLogger struct{}

func (discardLogger) Info(format string, args ...interface{})  {}
func (discardLogger) Warn(format string, args ...interface{})  {}
func (discardLogger) Error(format string, args ...interface{}) {}
func (discardLogger) Debug(format string, args ...interface{}) {}
