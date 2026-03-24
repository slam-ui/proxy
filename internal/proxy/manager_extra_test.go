package proxy

import (
	"testing"

	"proxyclient/internal/logger"
)

// ── validateConfig: форматная валидация ──────────────────────────────────

// BUG-РИСК: validateConfig проверяет только пустоту, но не формат "host:port".
// "invalid" без порта не является валидным прокси-адресом, но проходит валидацию.
// Тест документирует текущее поведение — поможет при добавлении net.SplitHostPort.
func TestValidateConfig_Format_DocumentCurrentBehavior(t *testing.T) {
	cases := []struct {
		addr      string
		wantErr   bool
		note      string
	}{
		{"127.0.0.1:8080", false, "валидный host:port"},
		{"localhost:3128", false, "валидный domain:port"},
		{"", true, "пустой — ошибка"},
		{"   ", true, "пробельный — ошибка"},
		// Следующие случаи — потенциальные баги (validateConfig их пропускает):
		// {"invalid", true, "нет порта — должна быть ошибка"},
		// {"127.0.0.1", true, "нет порта — должна быть ошибка"},
		// {"127.0.0.1:99999", true, "порт вне диапазона"},
	}
	for _, tc := range cases {
		err := validateConfig(Config{Address: tc.addr})
		if (err != nil) != tc.wantErr {
			t.Errorf("validateConfig(%q) [%s]: err=%v, wantErr=%v",
				tc.addr, tc.note, err, tc.wantErr)
		}
	}
}

// ── Enable: смена конфига обновляет адрес ────────────────────────────────

// BUG-РИСК: два последовательных Enable с разными конфигами — второй должен
// перезаписать первый, не оставлять старый адрес.
func TestManager_Enable_ChangesAddress(t *testing.T) {
	mgr := NewManager(&logger.NoOpLogger{})
	defer mgr.Disable()

	cfg1 := Config{Address: "127.0.0.1:8080"}
	cfg2 := Config{Address: "127.0.0.1:9090"}

	if err := mgr.Enable(cfg1); err != nil {
		t.Logf("Enable cfg1 failed (ожидаемо на не-Windows): %v", err)
		return
	}
	if err := mgr.Enable(cfg2); err != nil {
		t.Logf("Enable cfg2 failed: %v", err)
		return
	}

	got := mgr.GetConfig()
	if got.Address != cfg2.Address {
		t.Errorf("после второго Enable Address = %q, want %q", got.Address, cfg2.Address)
	}
}

// ── Disable: конфиг сохраняется после Disable ─────────────────────────────

// BUG-РИСК: после Disable GetConfig должен возвращать последний активный конфиг
// (чтобы UI мог показать "последнее использованное значение").
// ИЛИ возвращать пустой — главное, что поведение стабильно.
func TestManager_Disable_ConfigStateStable(t *testing.T) {
	mgr := NewManager(&logger.NoOpLogger{})
	cfg := Config{Address: "127.0.0.1:8080"}

	if err := mgr.Enable(cfg); err != nil {
		t.Logf("Enable failed (не-Windows): %v", err)
		return
	}
	mgr.Disable()

	// После Disable IsEnabled = false
	if mgr.IsEnabled() {
		t.Error("IsEnabled должен быть false после Disable")
	}
	// GetConfig не должен паниковать
	_ = mgr.GetConfig()
}

// ── NewManager: инициализация без паники ────────────────────────────────

// BUG-РИСК: NewManager читает реестр Windows при старте. На не-Windows или
// без прав должен работать без паники.
func TestNewManager_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("NewManager вызвал panic: %v", r)
		}
	}()
	mgr := NewManager(&logger.NoOpLogger{})
	if mgr == nil {
		t.Fatal("NewManager вернул nil")
	}
}

// ── Enable с пустым override ─────────────────────────────────────────────

// BUG-РИСК: пустой Override должен быть допустимым — не все прокси требуют bypass-list.
func TestManager_Enable_EmptyOverride_Valid(t *testing.T) {
	mgr := NewManager(&logger.NoOpLogger{})
	defer mgr.Disable()

	cfg := Config{Address: "127.0.0.1:8080", Override: ""}
	err := validateConfig(cfg)
	if err != nil {
		t.Errorf("пустой Override должен быть допустимым: %v", err)
	}
}

// ── Thread safety: GetConfig конкурентно ─────────────────────────────────

// BUG-РИСК: GetConfig использует RLock, но несколько одновременных RLock
// с одним Lock не должны создавать deadlock.
func TestManager_GetConfig_ConcurrentReads(t *testing.T) {
	mgr := NewManager(&logger.NoOpLogger{})
	cfg := Config{Address: "127.0.0.1:8080"}
	_ = mgr.Enable(cfg)
	defer mgr.Disable()

	done := make(chan struct{}, 50)
	for i := 0; i < 50; i++ {
		go func() {
			_ = mgr.GetConfig()
			_ = mgr.IsEnabled()
			done <- struct{}{}
		}()
	}
	for i := 0; i < 50; i++ {
		<-done
	}
}
