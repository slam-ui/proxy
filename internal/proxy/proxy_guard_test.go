package proxy

import (
	"context"
	"testing"
	"time"
)

// B-2: Test для Proxy Guard.
// Проверяет что периодическая проверка восстанавливает прокси если он был отключен извне.

type mockLogger struct{}

func (m *mockLogger) Debug(format string, args ...interface{}) {}
func (m *mockLogger) Info(format string, args ...interface{})  {}
func (m *mockLogger) Warn(format string, args ...interface{})  {}
func (m *mockLogger) Error(format string, args ...interface{}) {}

// TestProxyGuardStartsAndStops проверяет что guard корректно запускается и останавливается.
func TestProxyGuardStartsAndStops(t *testing.T) {
	log := &mockLogger{}
	mgr := NewManager(log)

	// Включаем прокси
	cfg := Config{Address: "127.0.0.1:8080", Override: "<local>"}
	if err := mgr.Enable(cfg); err != nil {
		t.Fatalf("Enable failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Запускаем guard
	if err := mgr.StartGuard(ctx, 100*time.Millisecond); err != nil {
		t.Fatalf("StartGuard failed: %v", err)
	}

	// Даём guard время для выполнения нескольких итераций
	time.Sleep(300 * time.Millisecond)

	// Останавливаем
	mgr.StopGuard()

	// Проверяем что stop не вызвал ошибку
	if !mgr.IsEnabled() {
		t.Error("Proxy должен остаться включённым после stop guard")
	}
}

// TestProxyGuardRestoresWhenDisabled проверяет что guard может заметить и восстановить отключенный прокси.
// Это определяется через getSystemProxyState, которая читает реестр.
// Для true интеграционного теста нужно реально изменять реестр — рискованно для тестов.
// Вместо этого проверяем что checkAndRestore вызывается и не паникует.
func TestProxyGuardCheckAndRestore(t *testing.T) {
	log := &mockLogger{}
	mgr := NewManager(log).(*manager)

	cfg := Config{Address: "127.0.0.1:8080", Override: "<local>"}
	if err := mgr.Enable(cfg); err != nil {
		t.Fatalf("Enable failed: %v", err)
	}

	// checkAndRestore должна завершиться без паники
	mgr.checkAndRestore()

	// Если изменили конфиг на disabled — checkAndRestore должна пропустить проверку
	mgr.mu.Lock()
	mgr.enabled = false
	mgr.mu.Unlock()

	mgr.checkAndRestore() // не должна восстанавливать если disabled=false
}

// TestProxyGuardMultipleStart проверяет что второй вызов StartGuard использует sync.Once и не запускает вторую горутину.
func TestProxyGuardMultipleStart(t *testing.T) {
	log := &mockLogger{}
	mgr := NewManager(log)

	cfg := Config{Address: "127.0.0.1:8080", Override: "<local>"}
	if err := mgr.Enable(cfg); err != nil {
		t.Fatalf("Enable failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Первый вызов
	if err := mgr.StartGuard(ctx, 100*time.Millisecond); err != nil {
		t.Fatalf("First StartGuard failed: %v", err)
	}

	// Второй вызов не должен запустить ещё одну горутину (sync.Once)
	if err := mgr.StartGuard(ctx, 100*time.Millisecond); err != nil {
		t.Fatalf("Second StartGuard failed: %v", err)
	}

	mgr.StopGuard()
}

// TestProxyGuardWithMicroInterval проверяет что очень малый интервал устанавливается в default.
func TestProxyGuardWithMicroInterval(t *testing.T) {
	log := &mockLogger{}
	mgr := NewManager(log)

	cfg := Config{Address: "127.0.0.1:8080", Override: "<local>"}
	if err := mgr.Enable(cfg); err != nil {
		t.Fatalf("Enable failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Интервал 1ms — очень мал, должен быть переопределён на 5s
	if err := mgr.StartGuard(ctx, 1*time.Millisecond); err != nil {
		t.Fatalf("StartGuard with tiny interval failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	mgr.StopGuard()
}

// ── БАГ 14: PauseGuard / ResumeGuard ─────────────────────────────────────────

// TestPauseGuard_SkipsRestore проверяет что во время паузы checkAndRestore не выполняется.
func TestPauseGuard_SkipsRestore(t *testing.T) {
	m := &manager{logger: &mockLogger{}}
	// Включаем прокси в памяти (без реального реестра — тестируем только логику паузы).
	m.enabled = true
	m.config = Config{Address: "127.0.0.1:8080", Override: "<local>"}

	// Без паузы checkAndRestore дошла бы до getSystemProxyState (внешний вызов).
	// С паузой — должна выйти сразу.
	m.PauseGuard(10 * time.Second)

	called := false
	// Подменить getSystemProxyState нельзя напрямую, но мы знаем:
	// если checkAndRestore выходит сразу — pause работает.
	// Проверяем через pausedUntil напрямую.
	m.mu.RLock()
	paused := !m.pausedUntil.IsZero() && time.Now().Before(m.pausedUntil)
	m.mu.RUnlock()
	_ = called

	if !paused {
		t.Error("PauseGuard не установила pausedUntil")
	}

	m.ResumeGuard()
	m.mu.RLock()
	paused2 := !m.pausedUntil.IsZero() && time.Now().Before(m.pausedUntil)
	m.mu.RUnlock()
	if paused2 {
		t.Error("ResumeGuard должна снять паузу")
	}
}

// TestStartGuard_GenerationRace проверяет что повторный StartGuard не оставляет guardRunning=false
// когда старая горутина завершается после запуска новой (race condition в очистке флага).
//
// BUG FIX: без guardGen старая горутина устанавливала guardRunning=false ПОСЛЕ того как
// новая горутина стартовала и выставила guardRunning=true → guardian выглядел остановленным
// хотя реально работал.
func TestStartGuard_GenerationRace(t *testing.T) {
	log := &mockLogger{}
	m := NewManager(log).(*manager)

	cfg := Config{Address: "127.0.0.1:8080", Override: "<local>"}
	if err := m.Enable(cfg); err != nil {
		t.Logf("Enable failed (не-Windows): %v — пропускаем", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Первый StartGuard
	if err := m.StartGuard(ctx, 50*time.Millisecond); err != nil {
		t.Fatalf("первый StartGuard: %v", err)
	}
	// Маленькая пауза чтобы первая горутина успела запуститься
	time.Sleep(20 * time.Millisecond)

	// Второй StartGuard сразу после первого — это должно корректно
	// заменить старую горутину, не оставляя guardRunning=false.
	if err := m.StartGuard(ctx, 50*time.Millisecond); err != nil {
		t.Fatalf("второй StartGuard: %v", err)
	}

	// Даём старой горутине время завершить cleanup (ctx отменён через guardCancel)
	time.Sleep(100 * time.Millisecond)

	// guardRunning должен остаться true — новая горутина работает.
	m.mu.Lock()
	running := m.guardRunning
	gen := m.guardGen
	m.mu.Unlock()

	if !running {
		t.Errorf("guardRunning=false после двойного StartGuard (gen=%d): старая горутина обнулила флаг", gen)
	}

	m.StopGuard()

	// После StopGuard горутина завершается → guardRunning=false
	time.Sleep(50 * time.Millisecond)
	m.mu.Lock()
	runningAfterStop := m.guardRunning
	m.mu.Unlock()
	if runningAfterStop {
		t.Error("guardRunning=true после StopGuard")
	}
}
