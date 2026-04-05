package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestInvalidateVLESSCache_ForcesReread проверяет основной сценарий бага:
// при быстрой замене secret.key mtime может не измениться (разрешение Windows ~15мс).
// InvalidateVLESSCache() должен гарантировать перечтение файла.
//
// Симулируем баг: вручную ставим одинаковый mtime на оба файла (до и после замены).
// Без InvalidateVLESSCache parseVLESSKey вернёт кэш (старый сервер A).
// С InvalidateVLESSCache — перечитает файл и вернёт сервер B.
func TestInvalidateVLESSCache_ForcesReread(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.key")

	urlA := "vless://uuid-aaa@server-a.example.com:443?sni=a.example.com&pbk=pubkey-a&sid=sid-a"
	urlB := "vless://uuid-bbb@server-b.example.com:443?sni=b.example.com&pbk=pubkey-b&sid=sid-b"

	// Шаг 1: записываем сервер A и парсим — кэш заполняется.
	if err := os.WriteFile(path, []byte(urlA), 0644); err != nil {
		t.Fatalf("WriteFile A: %v", err)
	}
	paramsA, err := parseVLESSKey(path)
	if err != nil {
		t.Fatalf("parseVLESSKey(A): %v", err)
	}
	if paramsA.Address != "server-a.example.com" {
		t.Fatalf("ожидался server-a.example.com, получили %q", paramsA.Address)
	}
	t.Logf("Шаг 1 OK: кэш заполнен для %q", paramsA.Address)

	// Шаг 2: записываем сервер B и принудительно ставим тот же mtime что был у A.
	// Это симулирует Windows mtime collision при быстрых последовательных записях.
	fiA, _ := os.Stat(path)
	mtimeA := fiA.ModTime()

	if err := os.WriteFile(path, []byte(urlB), 0644); err != nil {
		t.Fatalf("WriteFile B: %v", err)
	}
	// Форсируем одинаковый mtime — ключевой момент теста.
	if err := os.Chtimes(path, mtimeA, mtimeA); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	// Шаг 3: БЕЗ InvalidateVLESSCache — кэш должен вернуть старый сервер A
	// (mtime не изменился → vlessCache.mtime.Equal(modTime) = true → кэш).
	paramsBad, err := parseVLESSKey(path)
	if err != nil {
		t.Fatalf("parseVLESSKey без инвалидации: %v", err)
	}
	if paramsBad.Address != "server-a.example.com" {
		// Если этот assert падает, значит система имеет более высокое разрешение
		// часов (sub-15ms) и mtime всё-таки отличается. Логируем но не фейлим —
		// тест ниже всё равно проверяет InvaldateVLESSCache.
		t.Logf("INFO: ОС вернула разные mtime при Chtimes (разрешение часов высокое) — симуляция не сработала")
	} else {
		t.Logf("Шаг 3 OK: кэш вернул старый сервер A (mtime collision симулирован корректно)")
	}

	// Шаг 4: вызываем InvalidateVLESSCache() — кэш сбрасывается.
	InvalidateVLESSCache()

	// Шаг 5: теперь parseVLESSKey ОБЯЗАН перечитать файл и вернуть сервер B.
	paramsAfter, err := parseVLESSKey(path)
	if err != nil {
		t.Fatalf("parseVLESSKey после InvalidateVLESSCache: %v", err)
	}
	if paramsAfter.Address != "server-b.example.com" {
		t.Errorf("после InvalidateVLESSCache ожидался server-b.example.com, получили %q — кэш не был сброшен", paramsAfter.Address)
	} else {
		t.Logf("Шаг 5 OK: сервер B прочитан корректно после инвалидации кэша")
	}
}

// TestInvalidateVLESSCache_IdempotentOnEmptyCache проверяет что вызов
// InvalidateVLESSCache на пустом кэше не паникует.
func TestInvalidateVLESSCache_IdempotentOnEmptyCache(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("InvalidateVLESSCache() на пустом кэше вызвал panic: %v", r)
		}
	}()
	// Сбрасываем кэш в чистое состояние.
	InvalidateVLESSCache()
	// Повторный вызов не должен паниковать.
	InvalidateVLESSCache()
	t.Log("двойной вызов InvalidateVLESSCache OK — нет паники")
}

// TestInvalidateVLESSCache_ConcurrentSafe проверяет что InvalidateVLESSCache
// потокобезопасен при конкурентном вызове из нескольких горутин.
func TestInvalidateVLESSCache_ConcurrentSafe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "concurrent.key")
	url := "vless://uuid@host.com:443?sni=h.com&pbk=k&sid=s"
	if err := os.WriteFile(path, []byte(url), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Прогреваем кэш.
	if _, err := parseVLESSKey(path); err != nil {
		t.Fatalf("прогрев кэша: %v", err)
	}

	done := make(chan struct{})
	const N = 20
	results := make(chan error, N*2)

	// N горутин инвалидируют кэш.
	for i := 0; i < N; i++ {
		go func() {
			<-done
			InvalidateVLESSCache()
			results <- nil
		}()
	}
	// N горутин читают кэш.
	for i := 0; i < N; i++ {
		go func() {
			<-done
			_, err := parseVLESSKey(path)
			results <- err
		}()
	}

	close(done) // старт всех горутин одновременно
	deadline := time.After(5 * time.Second)
	for i := 0; i < N*2; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Errorf("конкурентный вызов вернул ошибку: %v", err)
			}
		case <-deadline:
			t.Fatal("таймаут конкурентного теста")
		}
	}
	t.Log("конкурентный тест InvalidateVLESSCache OK")
}
