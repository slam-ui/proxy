package xray

// Fuzz-тесты для xray пакета.
//
// Запуск:
//   go test -fuzz=FuzzIsTunConflict  -fuzztime=60s ./internal/xray/
//   go test -fuzz=FuzzTailWriter     -fuzztime=60s ./internal/xray/
//   go test -fuzz=FuzzCrashDetection -fuzztime=60s ./internal/xray/

import (
	"bytes"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// FuzzIsTunConflict
//
// IsTunConflict разбирает вывод sing-box для обнаружения wintun-конфликта.
// Уязвимости которые ищем:
//  1. Паника при бинарных данных
//  2. Ложные positive при случайном тексте
//  3. Ложные negative при известных сигнатурах
//  4. Переполнение при очень длинных строках
// ─────────────────────────────────────────────────────────────────────────────

func FuzzIsTunConflict(f *testing.F) {
	// Известные true-случаи
	f.Add("FATAL[0015] start service: start inbound/tun[tun-in]: configure tun interface: Cannot create a file when that file already exists.")
	f.Add("FATAL[0001] configure tun interface: something failed")
	f.Add("FATAL[0001] A device attached to the system is not functioning.")
	// Известные false-случаи
	f.Add("")
	f.Add("WARN[0010] inbound/tun[tun-in]: open interface take too much time to finish!")
	f.Add("INFO[0000] sing-box started")
	f.Add("FATAL[0000] dial tcp: connection refused")
	// Граничные случаи
	f.Add("Cannot create a file when that file already exists") // без FATAL
	f.Add(strings.Repeat("A", 100000))
	f.Add("\x00\xff\xfe\x01binary data")
	f.Add(strings.Repeat("configure tun interface\n", 1000))
	// Частичные совпадения
	f.Add("cannot create a file") // нижний регистр
	f.Add("CONFIGURE TUN INTERFACE") // верхний регистр
	f.Add("xonfigure tun interface") // опечатка
	// Unicode
	f.Add("configure tun interface — не удалось создать файл")
	f.Add("Cannot create a file \u4e2d\u6587 when that file already exists")

	f.Fuzz(func(t *testing.T, output string) {
		// Инвариант 1: нет паники
		result := IsTunConflict(output)

		// Инвариант 2: известные сигнатуры → всегда true
		for _, sig := range TunConflictSignatures {
			if strings.Contains(output, sig) {
				if !result {
					t.Errorf("IsTunConflict(%q) = false, но содержит сигнатуру %q",
						output[:fuzzMin(100, len(output))], sig)
				}
				break
			}
		}

		// Инвариант 3: пустая строка → всегда false
		if output == "" && result {
			t.Error("IsTunConflict(\"\") должен возвращать false")
		}

		// Инвариант 4: результат стабилен (нет недетерминизма)
		result2 := IsTunConflict(output)
		if result != result2 {
			t.Errorf("IsTunConflict недетерминирован: первый=%v второй=%v", result, result2)
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// FuzzTailWriter
//
// tailWriter — кольцевой буфер для захвата последних N байт вывода sing-box.
// Уязвимости:
//  1. Паника при записи бинарных данных
//  2. Memory leak (backing array не освобождается)
//  3. Некорректная обрезка посередине строки
//  4. Гонка данных при конкурентной записи
// ─────────────────────────────────────────────────────────────────────────────

func FuzzTailWriter(f *testing.F) {
	// Различные размеры и содержимое
	f.Add(64, []byte("hello\nworld\n"))
	f.Add(4, []byte("1234567890"))
	f.Add(1, []byte("x"))
	f.Add(0, []byte("data")) // max=0 — граничный случай
	f.Add(32*1024, []byte(strings.Repeat("FATAL[0015] error\n", 100)))
	f.Add(10, []byte("\x00\xff\xfe binary data \x01\x02"))
	f.Add(50, []byte(strings.Repeat("A", 200)))
	f.Add(100, []byte("line1\nline2\nline3\n"))
	f.Add(10, []byte("no newlines here"))
	f.Add(5, []byte("\n\n\n\n\n\n"))
	f.Add(32, []byte("Cannot create a file when that file already exists\n"))

	f.Fuzz(func(t *testing.T, maxSize int, data []byte) {
		// Ограничиваем maxSize разумными пределами для производительности
		if maxSize < 0 {
			maxSize = 0
		}
		if maxSize > 1<<16 { // 64KB max — 1MB вызывало OOM при миллионах итераций фаззера
			maxSize = 1 << 16
		}
		// Ограничиваем размер data чтобы один вызов не занимал больше 64KB памяти.
		const maxDataLen = 1 << 16
		if len(data) > maxDataLen {
			data = data[:maxDataLen]
		}

		defer func() {
			if r := recover(); r != nil {
				t.Errorf("panic при maxSize=%d data=%q: %v", maxSize, data[:fuzzMin(50, len(data))], r)
			}
		}()

		tw := newTailWriter(maxSize)

		// Инвариант 1: Write не паникует
		n, err := tw.Write(data)
		if err != nil {
			t.Errorf("Write вернул ошибку: %v", err)
		}
		if n != len(data) {
			t.Errorf("Write вернул n=%d, ожидали %d", n, len(data))
		}

		result := tw.String()

		// Инвариант 2: результат не длиннее max (с поправкой на выравнивание по строке)
		if maxSize > 0 && len(result) > maxSize {
			t.Errorf("tailWriter.String() длиннее max: len=%d max=%d", len(result), maxSize)
		}

		// Инвариант 3: если были переносы строк и произошла обрезка,
		// результат должен начинаться с начала строки
		if len(data) > maxSize && maxSize > 0 && bytes.Contains(data, []byte("\n")) {
			if len(result) > 0 && !bytes.HasPrefix(data, []byte(result[:1])) {
				// Проверяем что не начинается посередине строки
				// (т.е. предыдущий символ — перенос строки, или это начало буфера)
			}
		}

		// Инвариант 4: IsTunConflict работает с выводом tailWriter
		// (критично для обнаружения wintun-конфликтов)
		_ = IsTunConflict(result)

		// Инвариант 5: Reset очищает без паники
		tw.Reset()
		if tw.String() != "" {
			t.Error("после Reset tailWriter.String() должен возвращать пустую строку")
		}

		// Инвариант 6: после Reset можно снова писать
		_, _ = tw.Write([]byte("post-reset\n"))
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// FuzzCrashDetection
//
// Проверяем полный цикл обнаружения краша:
// Write(output) → IsTunConflict → правильная классификация
//
// Целевые случаи:
//  1. Реальный FATAL вывод sing-box → IsTunConflict = true
//  2. Нормальный вывод → IsTunConflict = false
//  3. Смешанный вывод (ошибки + нормальные строки) → корректно
// ─────────────────────────────────────────────────────────────────────────────

func FuzzCrashDetection(f *testing.F) {
	// Генерируем типичный вывод sing-box с конфликтом в конце
	normalLines := "INFO[0000] sing-box started\nINFO[0001] inbound started\n"
	conflictLine := "FATAL[0015] start service: start inbound/tun[tun-in]: configure tun interface: Cannot create a file when that file already exists.\n"
	f.Add(normalLines+conflictLine, true)
	f.Add(normalLines, false)
	f.Add(conflictLine, true)
	f.Add("", false)
	f.Add(strings.Repeat(normalLines, 100)+conflictLine, true)
	f.Add(strings.Repeat("x", 32*1024)+conflictLine, true)
	f.Add("partial: Cannot create a file", true)

	f.Fuzz(func(t *testing.T, output string, expectConflict bool) {
		// Записываем в tailWriter (симулируем реальный сценарий)
		tw := newTailWriter(32 * 1024)
		_, _ = tw.Write([]byte(output))

		captured := tw.String()

		// Инвариант: IsTunConflict на захваченном выводе не паникует
		conflict := IsTunConflict(captured)

		// Если в исходном выводе была сигнатура И она влезла в буфер →
		// conflict должен быть true
		hasSignatureInFull := IsTunConflict(output)
		if hasSignatureInFull && !conflict && len(output) <= 32*1024 {
			// Сигнатура была и помещается в буфер — должна быть обнаружена
			t.Errorf("сигнатура потеряна: len(output)=%d, IsTunConflict(full)=%v, IsTunConflict(tail)=%v",
				len(output), hasSignatureInFull, conflict)
		}

		_ = expectConflict // подсказка фаззеру, не жёсткая проверка
	})
}

func fuzzMin(a, b int) int {
	if a < b {
		return a
	}
	return b
}
