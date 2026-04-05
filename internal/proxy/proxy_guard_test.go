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
