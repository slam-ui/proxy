//go:build windows

package wintun_test

// Fuzz-тесты для wintun пакета.
//
// Запуск:
//   go test -fuzz=FuzzStopFileContent  -fuzztime=60s ./internal/wintun/
//   go test -fuzz=FuzzGapFileContent   -fuzztime=60s ./internal/wintun/
//   go test -fuzz=FuzzMarkerSequence   -fuzztime=60s ./internal/wintun/
//   go test -fuzz=FuzzPollUntilFreeFiles -fuzztime=60s ./internal/wintun/

import (
	"context"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"proxyclient/internal/wintun"
)

// ─────────────────────────────────────────────────────────────────────────────
// FuzzStopFileContent
//
// Проверяем что произвольное содержимое StopFile:
//  1. Не вызывает паник в ReadAdaptiveGap, EstimateReadyAt, PollUntilFree
//  2. Не вызывает бесконечного ожидания в PollUntilFree
//  3. Все функции возвращают разумные значения
// ─────────────────────────────────────────────────────────────────────────────

func FuzzStopFileContent(f *testing.F) {
	// Seed: различные форматы содержимого StopFile
	validNow := strconv.FormatInt(time.Now().UnixNano(), 10)
	validOld := strconv.FormatInt(time.Now().Add(-10*time.Minute).UnixNano(), 10)

	f.Add([]byte(validNow))
	f.Add([]byte(validOld))
	f.Add([]byte("0"))
	f.Add([]byte("-1"))
	f.Add([]byte(""))
	f.Add([]byte("not-a-number"))
	f.Add([]byte("9999999999999999999999")) // overflow int64
	f.Add([]byte("1\x00garbage"))
	f.Add([]byte("\n\r\t123456789"))
	f.Add([]byte(strings.Repeat("9", 100)))
	f.Add([]byte(strconv.FormatInt(math.MaxInt64, 10)))
	f.Add([]byte(strconv.FormatInt(math.MinInt64, 10)))
	f.Add([]byte("\xef\xbb\xbf" + validNow)) // BOM
	f.Add([]byte("  " + validNow + "  "))    // пробелы
	f.Add([]byte("1.5e10"))                  // float

	f.Fuzz(func(t *testing.T, content []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("panic при StopFile=%q: %v", content, r)
			}
		}()

		dir := t.TempDir()
		old, _ := os.Getwd()
		_ = os.Chdir(dir)
		defer func() { _ = os.Chdir(old) }()

		_ = os.WriteFile(wintun.StopFile, content, 0644)

		// Инвариант 1: ReadAdaptiveGap не паникует и возвращает разумное значение
		gap := wintun.ReadAdaptiveGap()
		if gap < 0 {
			t.Errorf("ReadAdaptiveGap вернул отрицательный gap=%v при StopFile=%q", gap, content)
		}
		if gap > 10*time.Minute {
			t.Errorf("ReadAdaptiveGap вернул слишком большой gap=%v при StopFile=%q", gap, content)
		}

		// Инвариант 2: EstimateReadyAt не паникует
		eta := wintun.EstimateReadyAt()
		_ = eta

		// Инвариант 3: PollUntilFree завершается при отмене ctx (не зависает)
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		wintun.PollUntilFree(ctx, &silentLog{}, "tun0")
		// Если зависнет дольше 300мс — ctx timeout поймает
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// FuzzGapFileContent
//
// Произвольное содержимое gap-файла не должно ломать ReadAdaptiveGap.
// Инварианты:
//  1. Нет паники
//  2. Результат всегда в [minGapBase=60s, minGapMax=3m]
//  3. Нет бесконечного цикла
// ─────────────────────────────────────────────────────────────────────────────

func FuzzGapFileContent(f *testing.F) {
	f.Add([]byte("60000000000"))  // 60s в наносекундах
	f.Add([]byte("180000000000")) // 3m
	f.Add([]byte("1"))
	f.Add([]byte("-1"))
	f.Add([]byte(""))
	f.Add([]byte("NaN"))
	f.Add([]byte("Inf"))
	f.Add([]byte("+Inf"))
	f.Add([]byte("1e100"))
	f.Add([]byte(strings.Repeat("9", 50)))
	f.Add([]byte("\xff\xfe\x00\x01")) // binary
	f.Add([]byte("0\n60000000000"))   // многострочный
	f.Add([]byte(strconv.FormatInt(math.MaxInt64, 10)))

	f.Fuzz(func(t *testing.T, content []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("panic при gap file=%q: %v", content, r)
			}
		}()

		dir := t.TempDir()
		old, _ := os.Getwd()
		_ = os.Chdir(dir)
		defer func() { _ = os.Chdir(old) }()

		_ = os.WriteFile("wintun_gap_ns", content, 0644)

		gap := wintun.ReadAdaptiveGap()

		// Инвариант: gap всегда в [60s, 3m]
		if gap < 60*time.Second {
			t.Errorf("gap=%v < minGapBase=60s при файле=%q", gap, content)
		}
		if gap > 3*time.Minute {
			t.Errorf("gap=%v > minGapMax=3m при файле=%q", gap, content)
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// FuzzMarkerSequence
//
// Произвольная последовательность операций с маркерными файлами.
// Проверяем что любая последовательность не нарушает инварианты.
//
// Операции кодируются как биты в uint8:
//  bit 0: RecordStop
//  bit 1: RecordCleanShutdown
//  bit 2: os.Remove(StopFile)
//  bit 3: os.Remove(CleanShutdownFile)
//  bit 4: os.Remove(FastDeleteFile)
//  bit 5: WriteFile(FastDeleteFile)
//  bit 6: IncreaseAdaptiveGap
//  bit 7: ResetAdaptiveGap
// ─────────────────────────────────────────────────────────────────────────────

func FuzzMarkerSequence(f *testing.F) {
	f.Add(uint8(0b00000001)) // только RecordStop
	f.Add(uint8(0b00000011)) // RecordStop + RecordCleanShutdown
	f.Add(uint8(0b11111111)) // все операции
	f.Add(uint8(0b00000000)) // ничего
	f.Add(uint8(0b01000001)) // RecordStop + ResetAdaptiveGap
	f.Add(uint8(0b00100010)) // CleanShutdown + IncreaseGap
	f.Add(uint8(0b00110011)) // перемешанные

	f.Fuzz(func(t *testing.T, ops uint8) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("panic при ops=0b%08b: %v", ops, r)
			}
		}()

		dir := t.TempDir()
		old, _ := os.Getwd()
		_ = os.Chdir(dir)
		defer func() { _ = os.Chdir(old) }()

		// Выполняем операции по битам
		if ops&(1<<0) != 0 {
			wintun.RecordStop()
		}
		if ops&(1<<1) != 0 {
			wintun.RecordCleanShutdown()
		}
		if ops&(1<<2) != 0 {
			_ = os.Remove(wintun.StopFile)
		}
		if ops&(1<<3) != 0 {
			_ = os.Remove(wintun.CleanShutdownFile)
		}
		if ops&(1<<4) != 0 {
			_ = os.Remove(wintun.FastDeleteFile)
		}
		if ops&(1<<5) != 0 {
			_ = os.WriteFile(wintun.FastDeleteFile, []byte("1"), 0644)
		}
		if ops&(1<<6) != 0 {
			wintun.IncreaseAdaptiveGap(&silentLog{})
		}
		if ops&(1<<7) != 0 {
			wintun.ResetAdaptiveGap()
		}

		// После любой последовательности операций:
		// Инвариант 1: EstimateReadyAt не паникует
		_ = wintun.EstimateReadyAt()

		// Инвариант 2: ReadAdaptiveGap в допустимых границах
		gap := wintun.ReadAdaptiveGap()
		if gap < 60*time.Second || gap > 3*time.Minute {
			t.Errorf("gap=%v вне границ [60s, 3m] после ops=0b%08b", gap, ops)
		}

		// Инвариант 3: если RecordStop вызван → CleanShutdownFile отсутствует
		if ops&(1<<0) != 0 && ops&(1<<1) == 0 {
			// RecordStop без последующего RecordCleanShutdown
			if _, err := os.Stat(wintun.CleanShutdownFile); err == nil {
				t.Errorf("CleanShutdownFile должен быть удалён после RecordStop (ops=0b%08b)", ops)
			}
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// FuzzPollUntilFreeFiles
//
// Проверяем PollUntilFree при произвольном содержимом всех файлов состояния.
// Главный инвариант: функция ВСЕГДА возвращается при отмене ctx.
// ─────────────────────────────────────────────────────────────────────────────

func FuzzPollUntilFreeFiles(f *testing.F) {
	// stopContent, gapContent, hasCleanShutdown, hasFastDelete
	f.Add([]byte(""), []byte(""), false, false)
	f.Add([]byte(strconv.FormatInt(time.Now().UnixNano(), 10)), []byte(""), true, false)
	f.Add([]byte(strconv.FormatInt(time.Now().UnixNano(), 10)), []byte("60000000000"), false, true)
	f.Add([]byte("bad"), []byte("bad"), true, true)
	f.Add([]byte(strconv.FormatInt(time.Now().Add(-1*time.Hour).UnixNano(), 10)), []byte(""), false, false)

	f.Fuzz(func(t *testing.T, stopContent, gapContent []byte, hasCleanShutdown, hasFastDelete bool) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("panic: stop=%q gap=%q clean=%v fast=%v: %v",
					stopContent, gapContent, hasCleanShutdown, hasFastDelete, r)
			}
		}()

		dir := t.TempDir()
		old, _ := os.Getwd()
		_ = os.Chdir(dir)
		defer func() { _ = os.Chdir(old) }()

		if len(stopContent) > 0 {
			_ = os.WriteFile(wintun.StopFile, stopContent, 0644)
		}
		if len(gapContent) > 0 {
			_ = os.WriteFile("wintun_gap_ns", gapContent, 0644)
		}
		if hasCleanShutdown {
			_ = os.WriteFile(wintun.CleanShutdownFile, []byte("1"), 0644)
		}
		if hasFastDelete {
			_ = os.WriteFile(wintun.FastDeleteFile, []byte("1"), 0644)
		}

		// ГЛАВНЫЙ ИНВАРИАНТ: ctx отмена всегда прерывает функцию
		ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
		defer cancel()

		done := make(chan struct{}, 1)
		go func() {
			wintun.PollUntilFree(ctx, &silentLog{}, "tun0")
			done <- struct{}{}
		}()

		select {
		case <-done:
			// OK — завершилась (либо быстрый путь, либо отмена ctx)
		case <-time.After(600 * time.Millisecond):
			t.Errorf("PollUntilFree зависла: stop=%q gap=%q clean=%v fast=%v",
				stopContent, gapContent, hasCleanShutdown, hasFastDelete)
		}
	})
}
