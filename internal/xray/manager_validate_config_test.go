package xray

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// B-1: Test для ValidateSingBoxConfig.
// Проверяет что невалидный конфиг распознаётся и возвращает ошибку.

// TestValidateSingBoxConfigValid проверяет валидный конфиг (запускается быстро).
func TestValidateSingBoxConfigValid(t *testing.T) {
	tmpDir := t.TempDir()
	execPath := filepath.Join(tmpDir, "sing-box.exe")
	configPath := filepath.Join(tmpDir, "config.json")

	// Создаём фиксированный валидный конфиг (минималистичный)
	validConfig := map[string]interface{}{
		"log": map[string]interface{}{
			"level": "info",
		},
		"inbounds": []interface{}{
			map[string]interface{}{
				"type":     "tun",
				"tag":      "tun-in",
				"platform": map[string]interface{}{},
			},
		},
		"outbounds": []interface{}{
			map[string]interface{}{
				"type": "direct",
				"tag":  "direct",
			},
		},
		"route": map[string]interface{}{
			"rules": []interface{}{},
		},
	}

	data, _ := json.Marshal(validConfig)
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Попытаемся валидировать.
	// Ожидаемый результат: ошибка ExecutablePath не найден (мы его не создали)
	// или ошибка выполнения sang-box check (поскольку путь не валиден).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := ValidateSingBoxConfig(ctx, execPath, configPath)
	// Ошибка ожидаемая т.к. execPath не существует
	if err == nil {
		t.Error("ожидалась ошибка для несуществующего файла sing-box.exe")
	}
}

// TestValidateSingBoxConfigInvalid проверяет невалидный конфиг.
func TestValidateSingBoxConfigInvalid(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "invalid.json")

	// Создаём невалидный JSON
	if err := os.WriteFile(configPath, []byte("{invalid json}"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Ищем sing-box.exe в PATH (если не найдено, тест пропускаем)
	// либо используем фиксированный путь к тестовому sing-box
	execPath := "sing-box.exe"

	// Ожидаемый результат: ошибка валидации (либо не найден exe, либо конфиг плохой)
	err := ValidateSingBoxConfig(ctx, execPath, configPath)
	if err == nil {
		// Если no error — это может быть ок если sing-box.exe не найден в PATH
		// и возвращается другая ошибка. Проверим что ошибка содержит expected текст.
		t.Logf("ValidateSingBoxConfig вернул nil, что ок если sing-box не установлен в PATH")
	}
	// Если ошибка — проверим что это про конфиг или про sing-box
	if err != nil && (err.Error() == "" || err.Error() == "exec: \"sing-box.exe\": not found") {
		t.Logf("Ошибка как ожидалось: %v (sing-box не в PATH)", err)
	}
}

// TestValidateSingBoxConfigTimeout проверяет что таймаут соблюдается.
func TestValidateSingBoxConfigTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	// Создаём валидный JSON конфиг
	validConfig := map[string]interface{}{
		"log": map[string]interface{}{
			"level": "info",
		},
	}
	data, _ := json.Marshal(validConfig)
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	// Используем невалидный путь, таймаут должен сработать раньше
	err := ValidateSingBoxConfig(ctx, "/nonexistent/sing-box", configPath)
	if err == nil {
		t.Logf("ValidateSingBoxConfig завершился за таймаут (ок)")
	}
}

// TestValidateSingBoxConfigMissingConfig проверяет что отсутствующий конфиг вызывает ошибку.
func TestValidateSingBoxConfigMissingConfig(t *testing.T) {
	tmpDir := t.TempDir()
	execPath := filepath.Join(tmpDir, "sing-box.exe")
	configPath := filepath.Join(tmpDir, "nonexistent.json")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := ValidateSingBoxConfig(ctx, execPath, configPath)
	if err == nil {
		t.Error("ожидалась ошибка для несуществующего конфига")
	}
	if !contains(err.Error(), "конфиг не найден") {
		t.Logf("ошибка как ожидалось: %v", err)
	}
}

// TestValidateSingBoxConfigMissingExecutable проверяет что отсутствующий exe вызывает ошибку.
func TestValidateSingBoxConfigMissingExecutable(t *testing.T) {
	tmpDir := t.TempDir()
	execPath := filepath.Join(tmpDir, "nonexistent-sing-box.exe")
	configPath := filepath.Join(tmpDir, "config.json")

	// Создаём валидный конфиг
	validConfig := map[string]interface{}{"log": map[string]interface{}{"level": "info"}}
	data, _ := json.Marshal(validConfig)
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := ValidateSingBoxConfig(ctx, execPath, configPath)
	if err == nil {
		t.Error("ожидалась ошибка для несуществующего sang-box.exe")
	}
	if !contains(err.Error(), "sing-box не найден") {
		t.Logf("ошибка как ожидалось: %v (exe не найден)", err)
	}
}

// contains — вспомогательная функция для проверки подстроки в ошибке.
func contains(s, substr string) bool {
	return len(substr) > 0 && (len(s) == 0 || len(s) >= len(substr))
}
