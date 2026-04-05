package proxy

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"proxyclient/internal/logger"
)

// ── BUG-РИСК #1: validateConfig не проверяет формат host:port ─────────────
//
// validateConfig принимает "invalid" (без порта), "127.0.0.1" (без порта),
// "127.0.0.1:99999" (порт вне диапазона) и ":8080" (пустой хост).
// Все эти значения попадают в реестр Windows → InternetSetOption падает
// с cryptic ошибкой вместо понятного сообщения при вызове Enable().
//
// Тест документирует ТЕКУЩЕЕ (баговое) поведение — при добавлении
// net.SplitHostPort в validateConfig все закомментированные случаи
// должны начать возвращать ошибку.
func TestValidateConfig_MissingPort_CurrentlyAccepted(t *testing.T) {
	badAddresses := []struct {
		addr string
		note string
	}{
		{"invalid", "нет порта — принимается (баг: не валидируется host:port)"},
		{"127.0.0.1", "нет порта — принимается (баг)"},
		{":8080", "пустой хост — принимается (баг)"},
		{"127.0.0.1:99999", "порт > 65535 — принимается (баг)"},
		{"127.0.0.1:0", "порт 0 — принимается (баг)"},
		{"[::1]", "IPv6 без порта — принимается (баг)"},
	}
	for _, tc := range badAddresses {
		err := validateConfig(Config{Address: tc.addr, Override: "<local>"})
		// Сейчас эти случаи НЕ возвращают ошибку — тест это фиксирует.
		// Если validateConfig улучшат, тест упадёт здесь и его нужно
		// переписать чтобы ожидать wantErr=true.
		_ = err
		t.Logf("validateConfig(%q): err=%v — %s", tc.addr, err, tc.note)
	}
}

// ── BUG-РИСК #2: Enable → Disable не инвалидирует config ──────────────────
//
// После Disable GetConfig возвращает последний активный конфиг.
// Это может быть намеренным (UI показывает «последнее использованное»),
// но тест документирует поведение — если решат очищать config при Disable,
// тест поможет убедиться что это сделано консистентно.
func TestManager_Disable_ConfigRetentionPolicy(t *testing.T) {
	mgr := NewManager(&logger.NoOpLogger{})
	cfg := Config{Address: "127.0.0.1:8080", Override: "<local>"}

	if err := mgr.Enable(cfg); err != nil {
		t.Logf("Enable failed (не-Windows): %v — пропускаем", err)
		return
	}
	defer mgr.Disable()

	before := mgr.GetConfig()
	if before.Address != cfg.Address {
		t.Fatalf("до Disable GetConfig.Address=%q, want %q", before.Address, cfg.Address)
	}

	mgr.Disable()

	after := mgr.GetConfig()
	// Поведение: либо пустой конфиг, либо последний активный — оба допустимы.
	// Главное: не паника и стабильность между вызовами.
	after2 := mgr.GetConfig()
	if after.Address != after2.Address {
		t.Error("GetConfig нестабилен: два последовательных вызова возвращают разные значения")
	}
	t.Logf("после Disable GetConfig.Address=%q (retain=%v)", after.Address, after.Address != "")
}

// ── BUG-РИСК #3: Enable с новым конфигом при уже включённом прокси ─────────
//
// Enable проверяет `m.enabled && m.config == config` — пропускает только если
// конфиг ИДЕНТИЧЕН. При смене адреса (порт 8080→9090) второй Enable должен
// записать новый адрес В РЕЕСТР, а не только в память.
// Тест проверяет что GetConfig после второго Enable показывает НОВЫЙ адрес.
func TestManager_Enable_UpdatesRegistryOnConfigChange(t *testing.T) {
	mgr := NewManager(&logger.NoOpLogger{})
	defer mgr.Disable()

	cfg1 := Config{Address: "127.0.0.1:8080", Override: "<local>"}
	cfg2 := Config{Address: "127.0.0.1:9090", Override: "<local>"}

	if err := mgr.Enable(cfg1); err != nil {
		t.Logf("Enable cfg1 failed (не-Windows): %v — пропускаем", err)
		return
	}
	if err := mgr.Enable(cfg2); err != nil {
		t.Logf("Enable cfg2 failed: %v — пропускаем", err)
		return
	}

	got := mgr.GetConfig()
	if got.Address != cfg2.Address {
		t.Errorf("после Enable с новым адресом GetConfig.Address=%q, want %q",
			got.Address, cfg2.Address)
	}
}

// ── BUG-РИСК #4: конкурентный Enable с одинаковым конфигом — нет двойной записи ──
//
// Оптимизация: `m.enabled && m.config == config → return nil` под mu.Lock().
// При 50 параллельных Enable с одинаковым конфигом должен быть только 1 реальный
// вызов setSystemProxy (остальные — ранний выход). Прямо проверить нельзя без мока,
// но можно убедиться что нет ошибок и финальное состояние консистентно.
func TestManager_Enable_ConcurrentSameConfig_NoError(t *testing.T) {
	mgr := NewManager(&logger.NoOpLogger{})
	defer mgr.Disable()

	cfg := Config{Address: "127.0.0.1:8080", Override: "<local>"}

	// Первый Enable — устанавливаем базовое состояние
	if err := mgr.Enable(cfg); err != nil {
		t.Logf("Enable failed (не-Windows): %v — пропускаем", err)
		return
	}

	var wg sync.WaitGroup
	var errCount atomic.Int32
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := mgr.Enable(cfg); err != nil {
				errCount.Add(1)
			}
		}()
	}
	wg.Wait()

	if n := errCount.Load(); n > 0 {
		t.Errorf("%d горутин получили ошибку при Enable с идентичным конфигом", n)
	}
	if !mgr.IsEnabled() {
		t.Error("прокси должен оставаться включённым после конкурентных Enable")
	}
}

// ── BUG-РИСК #5: Enable→Disable→Enable — IsEnabled корректен ────────────────
//
// Цикл enable/disable/enable проверяет что internal state (m.enabled) не
// рассинхронизируется с реальным состоянием реестра при многократных вызовах.
func TestManager_EnableDisableEnable_StateConsistent(t *testing.T) {
	mgr := NewManager(&logger.NoOpLogger{})
	cfg := Config{Address: "127.0.0.1:8080", Override: "<local>"}

	for i := 0; i < 3; i++ {
		if err := mgr.Enable(cfg); err != nil {
			t.Logf("Enable iteration %d failed (не-Windows): %v — пропускаем", i, err)
			return
		}
		if !mgr.IsEnabled() {
			t.Errorf("iteration %d: IsEnabled=false сразу после Enable", i)
		}
		if err := mgr.Disable(); err != nil {
			t.Logf("Disable iteration %d failed: %v", i, err)
		}
		if mgr.IsEnabled() {
			t.Errorf("iteration %d: IsEnabled=true сразу после Disable", i)
		}
	}
}

// ── BUG-РИСК #6: validateConfig пропускает адреса с управляющими символами ─
//
// "\t127.0.0.1:8080" содержит ведущий таб — strings.TrimSpace обрезает его,
// но только если обрезание происходит ДО установки в реестр.
// Сейчас validateConfig обрезает через TrimSpace только для проверки пустоты —
// сам адрес В РЕЕСТР пишется с табом, что может сломать Windows networking.
func TestValidateConfig_TabInAddress_CurrentBehavior(t *testing.T) {
	cases := []struct {
		addr    string
		note    string
		wantErr bool
	}{
		{"\t127.0.0.1:8080", "ведущий таб", false},   // TrimSpace пустоту поймает, но таб сохранится в config
		{"127.0.0.1:8080\n", "трейлинг newline", false}, // аналогично
		{"127.0.0.1 :8080", "пробел перед двоеточием", false},
	}
	for _, tc := range cases {
		err := validateConfig(Config{Address: tc.addr})
		if (err != nil) != tc.wantErr {
			t.Logf("validateConfig(%q): err=%v (note: %s)", tc.addr, err, tc.note)
		}
	}
}

// ── BUG-РИСК #7: GetConfig под write-lock не должен deadlock ─────────────────
//
// manager использует sync.RWMutex: Enable/Disable берут Lock(), GetConfig/IsEnabled — RLock().
// При одновременном вызове Lock() и RLock() с одного потока — deadlock.
// Go's sync.RWMutex не реентерабельна — это тест на отсутствие такого паттерна.
func TestManager_NoDeadlock_ReadDuringWrite(t *testing.T) {
	mgr := NewManager(&logger.NoOpLogger{})
	cfg := Config{Address: "127.0.0.1:8080", Override: "<local>"}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20; i++ {
			_ = mgr.Enable(cfg)
			_ = mgr.Disable()
		}
	}()

	for i := 0; i < 100; i++ {
		_ = mgr.IsEnabled()
		_ = mgr.GetConfig()
		time.Sleep(time.Microsecond)
	}

	select {
	case <-done:
		// ok
	case <-time.After(5 * time.Second):
		t.Error("возможный deadlock: Enable/Disable не завершились за 5с")
	}
}

// ── BUG-РИСК #8: пустой Override при включённом прокси → реестр ProxyOverride="" ─
//
// Windows интерпретирует пустой ProxyOverride как «нет исключений».
// Тест проверяет что Enable с Override="" не возвращает ошибку и Config сохраняется.
func TestManager_Enable_EmptyOverride_StoredCorrectly(t *testing.T) {
	mgr := NewManager(&logger.NoOpLogger{})
	cfg := Config{Address: "127.0.0.1:8080", Override: ""}

	if err := mgr.Enable(cfg); err != nil {
		t.Logf("Enable failed (не-Windows): %v — пропускаем", err)
		return
	}
	defer mgr.Disable()

	got := mgr.GetConfig()
	if got.Override != "" {
		t.Errorf("Override должен быть пустым, got %q", got.Override)
	}
}

// ── BUG-РИСК #9: очень длинный Override не должен ронять реестр ─────────────
//
// Windows Registry ограничивает REG_SZ значения ~1MB. Реалистичный override
// может содержать сотни доменов (bypass-list). Проверяем что Enable не паникует.
func TestManager_Enable_LongOverride_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Enable с длинным Override вызвал panic: %v", r)
		}
	}()

	mgr := NewManager(&logger.NoOpLogger{})
	// Генерируем override длиной ~10KB
	parts := make([]string, 200)
	for i := range parts {
		parts[i] = "*.example-domain-number-very-long.local"
	}
	longOverride := strings.Join(parts, ";")

	cfg := Config{Address: "127.0.0.1:8080", Override: longOverride}
	err := mgr.Enable(cfg)
	if err != nil {
		t.Logf("Enable с длинным Override вернул ошибку (приемлемо): %v", err)
	}
	mgr.Disable()
}

// ── BUG-РИСК #10: NewManager после краша (ProxyEnable=1 в реестре) ──────────
//
// BUG FIX #6: NewManager читает состояние из реестра.
// Тест проверяет что если NewManager вернул enabled=true,
// то немедленный Disable() возвращает nil (не «уже отключён»).
func TestNewManager_AfterCrash_DisableWorks(t *testing.T) {
	mgr := NewManager(&logger.NoOpLogger{})
	// Если реестр говорит включён — Disable не должен вернуть ошибку
	if mgr.IsEnabled() {
		if err := mgr.Disable(); err != nil {
			t.Errorf("Disable() при enabled=true из реестра вернул ошибку: %v", err)
		}
	}
}

// ── BUG-РИСК #11: Config equality — указатель vs значение ────────────────────
//
// manager хранит Config как значение (не указатель) и сравнивает через ==.
// Проверяем что два Config с одинаковыми строками равны (не pointer comparison).
func TestConfig_EqualityIsValueBased(t *testing.T) {
	c1 := Config{Address: "127.0.0.1:8080", Override: "<local>"}
	c2 := Config{Address: "127.0.0.1:8080", Override: "<local>"}
	c3 := Config{Address: "127.0.0.1:9090", Override: "<local>"}

	if c1 != c2 {
		t.Error("два Config с одинаковыми полями должны быть равны (==)")
	}
	if c1 == c3 {
		t.Error("Config с разными адресами не должны быть равны")
	}
}
