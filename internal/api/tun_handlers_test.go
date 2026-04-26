package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"proxyclient/internal/config"
	"proxyclient/internal/xray"
)

// TestDoApply_ProxyRestoredAfterSuccessfulRestart — regression-тест для БАГ 1.
//
// Воспроизводит сценарий:
//  1. Прокси включён.
//  2. Пользователь меняет правила → запускается полный перезапуск (hot-reload отказывает).
//  3. doApply отключает прокси на время рестарта.
//  4. sing-box запускается успешно.
//  5. ОЖИДАЕМОЕ ПОВЕДЕНИЕ: прокси восстанавливается автоматически.
//
// До фикса: skipProxyRestore=true выставлялся без вызова Enable() —
// системный прокси Windows оставался выключен до следующего ручного перезапуска.
func TestDoApply_ProxyRestoredAfterSuccessfulRestart(t *testing.T) {
	_, h, cleanup := buildTunServer(t)
	defer cleanup()

	// Запускаем mock Clash API:
	//   PUT /configs → 500  (провалить hot-reload → форсируем полный restart-путь)
	//   GET /        → 200  (waitForSingBoxReady считает sing-box готовым)
	clashMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer clashMock.Close()

	// Перенаправляем Clash API на mock-сервер (пакетная переменная — в тестах безопасно).
	origURL := clashAPIBaseURL
	clashAPIBaseURL = clashMock.URL
	defer func() { clashAPIBaseURL = origURL }()

	// Инжектируем mock-менеджер вместо реального sing-box.
	mockMgr := &stubXray{running: true}
	h.newManagerFn = func(cfg xray.Config, ctx context.Context) (xray.Manager, error) {
		return mockMgr, nil
	}

	// Пропускаем ValidateSingBoxConfig (требует реального файла sing-box.exe).
	h.xrayConfig.ExecutablePath = ""

	// Создаём файлы конфига, необходимые для os.Rename в doApply.
	if err := os.WriteFile("config.singbox.json", []byte("{}"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile("config.singbox.json.pending", []byte("{}"), 0644); err != nil {
		t.Fatalf("WriteFile pending: %v", err)
	}
	h.xrayConfig.ConfigPath = "config.singbox.json"

	// Прокси был включён до apply — именно это состояние должно восстановиться.
	stubPM := h.proxyManager.(*stubProxy)
	stubPM.enabled = true

	// Инициализируем apply-состояние как будто его начал handleApply.
	h.apply.mu.Lock()
	h.apply.running = true
	h.apply.lastErr = ""
	h.apply.mu.Unlock()

	snapshot := &config.RoutingConfig{
		DefaultAction: config.ActionProxy,
	}

	go h.doApply(snapshot, "config.singbox.json.pending", false)

	// Ждём завершения doApply (максимум 10 секунд).
	completed := waitForApply(h, 10*time.Second)
	if !completed {
		t.Fatal("doApply не завершился за 10 секунд — возможно завис в waitForSingBoxReady")
	}

	h.apply.mu.Lock()
	lastErr := h.apply.lastErr
	h.apply.mu.Unlock()
	if lastErr != "" {
		t.Fatalf("doApply завершился с ошибкой: %s", lastErr)
	}

	// REGRESSION CHECK БАГ 1: прокси должен быть включён после успешного apply.
	if !h.proxyManager.IsEnabled() {
		t.Error("БАГ 1 REGRESSION: прокси остался выключен после успешного полного перезапуска")
	}
}

// TestHandleApply_ClearsValidationError — БАГ 13.
// Проверяет что validationError сбрасывается при каждом новом вызове handleApply,
// а не остаётся от предыдущего неудачного apply.
func TestHandleApply_ClearsValidationError(t *testing.T) {
	_, h, cleanup := buildTunServer(t)
	defer cleanup()

	// Имитируем предыдущую ошибку валидации (осталась от прошлого apply).
	h.apply.mu.Lock()
	h.apply.validationError = "предыдущая ошибка валидации"
	h.apply.running = false
	h.apply.mu.Unlock()

	// Вызываем handleApply. Он завершится с ошибкой (нет конфига), но до этого
	// должен очистить validationError.
	req := httptest.NewRequest(http.MethodPost, "/api/tun/apply", nil)
	w := httptest.NewRecorder()

	// handleApply очищает validationError в самом начале (внутри apply.mu.Lock).
	// Пускаем его и даём завершиться.
	h.handleApply(w, req)

	// Ждём пока doApply завершится (может быть запущен асинхронно).
	waitForApply(h, 5*time.Second)

	h.apply.mu.Lock()
	valErr := h.apply.validationError
	h.apply.mu.Unlock()

	// validationError должна быть либо пустой (успех) либо содержать НОВУЮ ошибку
	// этого apply — но точно не "предыдущая ошибка валидации".
	if valErr == "предыдущая ошибка валидации" {
		t.Error("БАГ 13: validationError не была сброшена при новом apply")
	}
}

// waitForApply ждёт пока h.apply.running станет false. Возвращает false при таймауте.
func waitForApply(h *TunHandlers, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		h.apply.mu.Lock()
		done := !h.apply.running
		h.apply.mu.Unlock()
		if done {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}
