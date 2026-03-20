//go:build windows

package fileutil_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"proxyclient/internal/fileutil"
)

// TestWriteAtomic_BasicRoundtrip проверяет базовое создание и чтение файла.
func TestWriteAtomic_BasicRoundtrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dst := filepath.Join(dir, "out.json")

	data := []byte(`{"key":"value"}`)
	if err := fileutil.WriteAtomic(dst, data, 0644); err != nil {
		t.Fatalf("WriteAtomic: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("got %q, want %q", got, data)
	}
}

// TestWriteAtomic_OverwritesExisting проверяет что повторный вызов заменяет файл.
func TestWriteAtomic_OverwritesExisting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dst := filepath.Join(dir, "out.json")

	if err := fileutil.WriteAtomic(dst, []byte("first"), 0644); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := fileutil.WriteAtomic(dst, []byte("second"), 0644); err != nil {
		t.Fatalf("second write: %v", err)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "second" {
		t.Errorf("got %q, want %q", got, "second")
	}
}

// TestWriteAtomic_Concurrent проверяет атомарность при конкурентных записях.
// Это прямое воспроизведение бага в window.go и apprules/storage.go:
// все горутины писали в один .tmp файл → два JSON конкатенировались в один.
func TestWriteAtomic_Concurrent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dst := filepath.Join(dir, "state.json")

	type state struct {
		X, Y, Width, Height int32
	}

	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			s := state{X: int32(n * 10), Y: int32(n * 5), Width: 960, Height: 640}
			data, _ := json.Marshal(s)
			if err := fileutil.WriteAtomic(dst, data, 0644); err != nil {
				// Ошибки при конкуренции возможны — важно что хотя бы одна горутина победила
				t.Logf("goroutine %d: %v", n, err)
			}
		}(i)
	}
	wg.Wait()

	raw, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("файл не создан: %v", err)
	}
	var got state
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Errorf("невалидный JSON после конкурентных записей: %v (содержимое: %q)", err, raw)
	}
	if got.Width != 960 || got.Height != 640 {
		t.Errorf("неожиданный размер: %+v", got)
	}
}

// TestWriteAtomic_NoLeftoverTmpFiles проверяет что tmp файлы не остаются на диске.
func TestWriteAtomic_NoLeftoverTmpFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dst := filepath.Join(dir, "out.json")

	for i := 0; i < 5; i++ {
		if err := fileutil.WriteAtomic(dst, []byte("data"), 0644); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "out.json" {
			t.Errorf("обнаружен лишний файл: %s", e.Name())
		}
	}
}
