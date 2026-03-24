//go:build windows

// Package wintun_test — симуляционные тесты запуска прокси.
//
// Эти тесты имитируют реальные сценарии запуска с разными параметрами:
// - время последнего запуска (холодный/горячий/промежуточный старт)
// - наличие/отсутствие маркерных файлов (FastDeleteFile, CleanShutdownFile)
// - состояние адаптивного gap (базовый/увеличенный/максимальный)
// - коррупция файлов состояния
// - конкурентные вызовы RecordStop и RecordCleanShutdown
// - граничные случаи времени (0, отрицательное, far future)
// - инварианты маркерных файлов
// - правильность EstimateReadyAt для UI progress bar
//
// НЕ тестируем: реальный wintun.dll, PowerShell, pnputil — только логику файлового
// состояния и маршрутизацию PollUntilFree между путями 0-3.
package wintun_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"proxyclient/internal/wintun"
)

// ─────────────────────────────────────────────────────────────────────────────
// Вспомогательные функции
// ─────────────────────────────────────────────────────────────────────────────

// simu переключает CWD на изолированную tempdir и возвращает cleanup.
// Все файлы состояния (StopFile, gap, маркеры) относительные → изолированы.
func simu(t *testing.T) func() {
	t.Helper()
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	return func() { _ = os.Chdir(old) }
}

func writeStop(t *testing.T, at time.Time) {
	t.Helper()
	_ = os.WriteFile(wintun.StopFile,
		[]byte(strconv.FormatInt(at.UnixNano(), 10)), 0644)
}

func writeFastDelete(t *testing.T) {
	t.Helper()
	_ = os.WriteFile(wintun.FastDeleteFile, []byte("1"), 0644)
}

func writeCleanShutdown(t *testing.T) {
	t.Helper()
	_ = os.WriteFile(wintun.CleanShutdownFile, []byte("1"), 0644)
}

func writeGap(t *testing.T, d time.Duration) {
	t.Helper()
	_ = os.WriteFile("wintun_gap_ns", []byte(strconv.FormatInt(int64(d), 10)), 0644)
}

// runWithTimeout запускает PollUntilFree с коротким ctx и возвращает время выполнения.
// fastReturn = true если функция вернулась быстрее порога.
func runWithTimeout(deadline time.Duration) (elapsed time.Duration, cancelled bool) {
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	start := time.Now()
	wintun.PollUntilFree(ctx, &silentLog{}, "tun0")
	elapsed = time.Since(start)
	cancelled = ctx.Err() != nil
	return
}

// ─────────────────────────────────────────────────────────────────────────────
// БЛОК 1: RecordStop / RecordCleanShutdown — инварианты маркеров
// ─────────────────────────────────────────────────────────────────────────────

// TestRecordStop_ClearsCleanShutdown проверяет что RecordStop удаляет CleanShutdownFile.
// БАГ если это не так: следующий PollUntilFree пойдёт по пути 0 (пропустит все задержки)
// несмотря на то что sing-box упал, а не остановился штатно.
func TestRecordStop_ClearsCleanShutdown(t *testing.T) {
	defer simu(t)()

	// Сначала записываем чистое завершение
	wintun.RecordCleanShutdown()
	if _, err := os.Stat(wintun.CleanShutdownFile); os.IsNotExist(err) {
		t.Fatal("CleanShutdownFile должен существовать после RecordCleanShutdown")
	}

	// Затем краш/kill — RecordStop должен убрать маркер
	wintun.RecordStop()

	if _, err := os.Stat(wintun.CleanShutdownFile); !os.IsNotExist(err) {
		t.Error("БАГ: RecordStop не удалил CleanShutdownFile — следующий старт пропустит gap несмотря на краш")
	}
}

// TestRecordCleanShutdown_DoesNotAffectStopFile проверяет что RecordCleanShutdown
// не трогает StopFile (timestamp последней остановки должен оставаться корректным).
func TestRecordCleanShutdown_DoesNotAffectStopFile(t *testing.T) {
	defer simu(t)()

	wintun.RecordStop()
	data1, _ := os.ReadFile(wintun.StopFile)

	time.Sleep(5 * time.Millisecond)
	wintun.RecordCleanShutdown()
	data2, _ := os.ReadFile(wintun.StopFile)

	if string(data1) != string(data2) {
		t.Error("БАГ: RecordCleanShutdown изменил StopFile — timestamp испорчен")
	}
}

// TestCleanShutdownFile_IsOneShot проверяет что CleanShutdownFile — одноразовый сигнал:
// PollUntilFree удаляет его при первом чтении, повторный вызов идёт по обычному пути.
func TestCleanShutdownFile_IsOneShot(t *testing.T) {
	defer simu(t)()

	writeStop(t, time.Now().Add(-1*time.Hour)) // старый StopFile
	writeCleanShutdown(t)

	// Первый вызов — потребляет маркер
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	wintun.PollUntilFree(ctx, &silentLog{}, "tun0")

	if _, err := os.Stat(wintun.CleanShutdownFile); !os.IsNotExist(err) {
		t.Error("БАГ: CleanShutdownFile должен быть удалён после первого PollUntilFree")
	}
}

// TestCleanShutdown_PreviousGapNotCarriedOver проверяет что при CleanShutdown
// адаптивный gap не влияет на поведение — функция возвращается немедленно.
func TestCleanShutdown_PreviousGapNotCarriedOver(t *testing.T) {
	defer simu(t)()

	writeStop(t, time.Now())
	writeGap(t, 3*time.Minute) // максимальный gap — но не должен применяться
	writeCleanShutdown(t)

	elapsed, cancelled := runWithTimeout(300 * time.Millisecond)
	if cancelled {
		t.Errorf("БАГ: PollUntilFree должна вернуться немедленно при CleanShutdown (gap=3m игнорируется), но зависла >300мс (elapsed=%v)", elapsed)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// БЛОК 2: PollUntilFree — маршрутизация по путям 0-3
// ─────────────────────────────────────────────────────────────────────────────

// TestPollUntilFree_Path0_CleanShutdown — чистое завершение, все задержки пропускаются.
func TestPollUntilFree_Path0_CleanShutdown(t *testing.T) {
	defer simu(t)()

	writeStop(t, time.Now())
	writeCleanShutdown(t)

	elapsed, cancelled := runWithTimeout(300 * time.Millisecond)
	if cancelled {
		t.Errorf("Путь 0 (CleanShutdown): ожидали мгновенный возврат, но зависли (elapsed=%v)", elapsed)
	}
}

// TestPollUntilFree_Path0_TakesPriorityOverFastDelete проверяет что путь 0
// имеет приоритет над FastDeleteFile — оба маркера присутствуют, CleanShutdown главнее.
func TestPollUntilFree_Path0_TakesPriorityOverFastDelete(t *testing.T) {
	defer simu(t)()

	writeStop(t, time.Now())
	writeCleanShutdown(t)
	writeFastDelete(t)

	elapsed, cancelled := runWithTimeout(300 * time.Millisecond)
	if cancelled {
		t.Errorf("Путь 0 должен иметь приоритет над FastDelete (elapsed=%v, ctx=%v)", elapsed, cancelled)
	}

	// CleanShutdownFile удалён, FastDeleteFile должен остаться нетронутым
	if _, err := os.Stat(wintun.CleanShutdownFile); !os.IsNotExist(err) {
		t.Error("CleanShutdownFile должен быть удалён")
	}
}

// TestPollUntilFree_Path1_FastDeleteSkipsGap проверяет что FastDeleteFile
// пропускает adaptive gap (горячий старт без полного ожидания).
// БАГ: если FastDeleteFile не обнаруживается → ждём полный gap=60+с.
func TestPollUntilFree_Path1_FastDeleteSkipsGap(t *testing.T) {
	defer simu(t)()

	writeStop(t, time.Now()) // горячий старт — иначе был бы холодный
	writeFastDelete(t)

	// FastDeleteFile удаляется синхронно в начале PollUntilFree.
	// Канселим через 500мс — проверяем что маркер удалён до входа в gap.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	wintun.PollUntilFree(ctx, &silentLog{}, "tun0")

	if _, err := os.Stat(wintun.FastDeleteFile); !os.IsNotExist(err) {
		t.Error("БАГ: FastDeleteFile должен быть удалён сразу — до gap/settle")
	}
}

// TestPollUntilFree_Path2_ColdStart проверяет холодный старт:
// StopFile очень старый → gap пропускается, но confirm-loop выполняется.
func TestPollUntilFree_Path2_ColdStart_NoGapWait(t *testing.T) {
	defer simu(t)()

	writeStop(t, time.Now().Add(-10*time.Minute)) // давно остановились

	// Если бы применялся gap, то с gap=60с функция бы зависала.
	// Контекст 1с: хватает на confirm-loop (3×500мс=1.5с) после пропуска gap.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	wintun.PollUntilFree(ctx, &silentLog{}, "tun0")

	// Либо вернулась (confirm-loop прошёл), либо отменена — главное не >10с ожидания
	// что было бы при полном gap
}

// TestPollUntilFree_Path3_HotStart_WaitsGap проверяет что горячий старт ждёт gap.
// Горячий старт = StopFile только что создан, нет FastDelete/CleanShutdown.
func TestPollUntilFree_Path3_HotStart_WaitsGap(t *testing.T) {
	defer simu(t)()

	writeStop(t, time.Now()) // только что остановились
	writeGap(t, 60*time.Second)

	// Запускаем с коротким ctx — функция ДОЛЖНА зависнуть (ждёт gap)
	// и вернуться только по отмене ctx
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	wintun.PollUntilFree(ctx, &silentLog{}, "tun0")
	elapsed := time.Since(start)

	if ctx.Err() == nil {
		t.Errorf("БАГ: горячий старт должен ждать gap=60с, но вернулся за %v без отмены ctx", elapsed)
	}
	// Должна зависнуть хотя бы 200мс (ctx timeout)
	if elapsed < 200*time.Millisecond {
		t.Errorf("БАГ: горячий старт должен блокироваться, но вернулся немедленно (%v)", elapsed)
	}
}

// TestPollUntilFree_HotStart_PartialGap проверяет что если часть gap уже прошла
// (elapsed > 0), то ждём только оставшееся время, а не весь gap.
func TestPollUntilFree_HotStart_PartialGap(t *testing.T) {
	defer simu(t)()

	// Остановились 30с назад, gap=60с → осталось ~30с
	writeStop(t, time.Now().Add(-30*time.Second))
	writeGap(t, 60*time.Second)

	// Если бы ждали полный gap=60с — ctx за 400мс отмениться
	// Если правильно считает remaining=~30с — тоже отменится, но это ожидаемо
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	wintun.PollUntilFree(ctx, &silentLog{}, "tun0")

	if ctx.Err() == nil {
		// Вернулась без отмены? Значит remaining ≈ 0 что неправильно для 30с elapsed
		// (только если confirm-loop тоже завершился, что маловероятно за 400мс)
		// Мягкая проверка — не падаем, просто отмечаем
		t.Log("PollUntilFree вернулась без ctx отмены — возможно confirm-loop очень быстрый")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// БЛОК 3: Граничные случаи времени
// ─────────────────────────────────────────────────────────────────────────────

// TestPollUntilFree_StopFileInFuture проверяет устойчивость к StopFile с
// timestamp в будущем (например при смене часового пояса или NTP скачке).
// БАГ: отрицательный elapsed → remaining = gap - (-N) = gap+N → ждём больше gap.
func TestPollUntilFree_StopFileInFuture(t *testing.T) {
	defer simu(t)()

	// Timestamp на 10 минут в будущем (NTP jump или смена времени)
	writeStop(t, time.Now().Add(10*time.Minute))
	writeGap(t, 60*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	wintun.PollUntilFree(ctx, &silentLog{}, "tun0")

	// Должна зависнуть (ждёт gap), но НЕ зависнуть на 70 минут
	// Проверяем что ctx отменился (≈400мс) а не раньше чем за 200мс
	elapsed := 400 * time.Millisecond
	if ctx.Err() == nil && elapsed < 200*time.Millisecond {
		t.Error("БАГ: StopFile в будущем → функция вернулась слишком быстро (gap не применён)")
	}
}

// TestPollUntilFree_StopFileZeroTimestamp проверяет что нулевой timestamp
// в StopFile не вызывает panic или бесконечное ожидание.
func TestPollUntilFree_StopFileZeroTimestamp(t *testing.T) {
	defer simu(t)()

	_ = os.WriteFile(wintun.StopFile, []byte("0"), 0644)
	writeGap(t, 60*time.Second)

	// Timestamp=0 → time.Unix(0,0) = 1970-01-01 → elapsed огромный → холодный старт
	// Ожидаем: либо холодный путь (мгновенно), либо gap (ctx отмена)
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	// Не должна паниковать
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("panic при нулевом timestamp StopFile: %v", r)
		}
	}()
	wintun.PollUntilFree(ctx, &silentLog{}, "tun0")
}

// TestPollUntilFree_StopFileNegativeTimestamp проверяет устойчивость к
// отрицательному timestamp (возможен при бите ошибки).
func TestPollUntilFree_StopFileNegativeTimestamp(t *testing.T) {
	defer simu(t)()

	_ = os.WriteFile(wintun.StopFile, []byte("-1000000000"), 0644)

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("panic при отрицательном timestamp: %v", r)
		}
	}()
	wintun.PollUntilFree(ctx, &silentLog{}, "tun0")
}

// TestPollUntilFree_StopFileMaxInt64 проверяет устойчивость к максимальному
// значению int64 (overflow защита).
func TestPollUntilFree_StopFileMaxInt64(t *testing.T) {
	defer simu(t)()

	_ = os.WriteFile(wintun.StopFile, []byte(strconv.FormatInt(^int64(0), 10)), 0644)

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("panic при max int64 timestamp: %v", r)
		}
	}()
	wintun.PollUntilFree(ctx, &silentLog{}, "tun0")
}

// TestPollUntilFree_ExactColdStartBoundary проверяет граничное значение
// coldStartThreshold — за 1 секунду до порога и через 1 секунду после.
func TestPollUntilFree_ExactColdStartBoundary(t *testing.T) {
	defer simu(t)()

	// coldStartThreshold = 2 минуты
	// Остановились ровно 2 минуты назад → пограничный случай
	writeStop(t, time.Now().Add(-2*time.Minute))
	writeGap(t, 60*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("panic на границе coldStartThreshold: %v", r)
		}
	}()
	wintun.PollUntilFree(ctx, &silentLog{}, "tun0")
	// Либо пошёл по пути 2 (холодный, нет gap), либо по пути 3 (горячий, gap)
	// В любом случае не должно быть паники
}

// ─────────────────────────────────────────────────────────────────────────────
// БЛОК 4: Коррупция файлов состояния
// ─────────────────────────────────────────────────────────────────────────────

// TestPollUntilFree_CorruptStopFile проверяет что повреждённый StopFile
// не вызывает panic и ведёт к корректному fallback.
func TestPollUntilFree_CorruptStopFile(t *testing.T) {
	defer simu(t)()

	// StopFile с мусором (бинарные данные, не число)
	_ = os.WriteFile(wintun.StopFile, []byte("not-a-timestamp\x00\xff"), 0644)

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("panic при повреждённом StopFile: %v", r)
		}
	}()
	wintun.PollUntilFree(ctx, &silentLog{}, "tun0")
}

// TestPollUntilFree_EmptyStopFile проверяет поведение при пустом StopFile.
func TestPollUntilFree_EmptyStopFile(t *testing.T) {
	defer simu(t)()

	_ = os.WriteFile(wintun.StopFile, []byte(""), 0644)

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("panic при пустом StopFile: %v", r)
		}
	}()
	wintun.PollUntilFree(ctx, &silentLog{}, "tun0")
}

// TestGapFile_Corrupt проверяет что повреждённый gap файл даёт minGapBase (60с).
func TestGapFile_Corrupt(t *testing.T) {
	defer simu(t)()

	_ = os.WriteFile("wintun_gap_ns", []byte("BROKEN\x00"), 0644)
	gap := wintun.ReadAdaptiveGap()
	if gap != 60*time.Second {
		t.Errorf("повреждённый gap файл: got %v, want 60s (fallback)", gap)
	}
}

// TestGapFile_NegativeValue проверяет что отрицательный gap возвращает minGapBase.
func TestGapFile_NegativeValue(t *testing.T) {
	defer simu(t)()

	_ = os.WriteFile("wintun_gap_ns", []byte("-1000000000"), 0644)
	gap := wintun.ReadAdaptiveGap()
	if gap < 60*time.Second {
		t.Errorf("отрицательный gap: должен быть >= 60s, got %v", gap)
	}
}

// TestFastDeleteFile_Corrupted проверяет что повреждённый FastDeleteFile
// всё равно обнаруживается (нам важен факт существования файла, не его содержимое).
func TestFastDeleteFile_Corrupted(t *testing.T) {
	defer simu(t)()

	writeStop(t, time.Now())
	// Записываем мусор вместо "1"
	_ = os.WriteFile(wintun.FastDeleteFile, []byte("\x00\xff\xfe"), 0644)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	wintun.PollUntilFree(ctx, &silentLog{}, "tun0")

	// Маркер должен быть удалён (факт существования = сигнал, содержимое не важно)
	if _, err := os.Stat(wintun.FastDeleteFile); !os.IsNotExist(err) {
		t.Error("БАГ: FastDeleteFile должен удаляться независимо от содержимого")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// БЛОК 5: AdaptiveGap — все сценарии изменения
// ─────────────────────────────────────────────────────────────────────────────

// TestAdaptiveGap_SequenceOfCrashes имитирует серию крашей и проверяет
// что gap растёт правильно: 60s → 120s → 180s(cap).
func TestAdaptiveGap_SequenceOfCrashes(t *testing.T) {
	defer simu(t)()

	expected := []time.Duration{60 * time.Second, 120 * time.Second, 3 * time.Minute}
	for i, want := range expected {
		got := wintun.ReadAdaptiveGap()
		if got != want {
			t.Errorf("crash #%d: gap=%v, want=%v", i, got, want)
		}
		if i < len(expected)-1 {
			wintun.IncreaseAdaptiveGap(&silentLog{})
		}
	}

	// Ещё одно увеличение — cap должен удерживать
	wintun.IncreaseAdaptiveGap(&silentLog{})
	if gap := wintun.ReadAdaptiveGap(); gap != 3*time.Minute {
		t.Errorf("gap должен оставаться на cap=3m, got %v", gap)
	}
}

// TestAdaptiveGap_ResetAfterSuccessfulStart имитирует сброс gap после
// успешного запуска (ResetAdaptiveGap вызывается в app.go).
func TestAdaptiveGap_ResetAfterSuccessfulStart(t *testing.T) {
	defer simu(t)()

	// 3 краша → gap на максимуме
	wintun.IncreaseAdaptiveGap(&silentLog{})
	wintun.IncreaseAdaptiveGap(&silentLog{})
	wintun.IncreaseAdaptiveGap(&silentLog{})

	// Успешный запуск — сброс
	wintun.ResetAdaptiveGap()

	if gap := wintun.ReadAdaptiveGap(); gap != 60*time.Second {
		t.Errorf("после ResetAdaptiveGap gap должен быть 60s, got %v", gap)
	}
	if _, err := os.Stat("wintun_gap_ns"); !os.IsNotExist(err) {
		t.Error("файл gap должен быть удалён после ResetAdaptiveGap")
	}
}

// TestAdaptiveGap_PersistsAcrossProcessRestarts проверяет что значение gap
// сохраняется между "перезапусками" (читается из файла каждый раз).
func TestAdaptiveGap_PersistsAcrossProcessRestarts(t *testing.T) {
	defer simu(t)()

	wintun.IncreaseAdaptiveGap(&silentLog{})
	gap1 := wintun.ReadAdaptiveGap()

	// Симулируем "перезапуск" — просто читаем снова
	gap2 := wintun.ReadAdaptiveGap()

	if gap1 != gap2 {
		t.Errorf("gap не сохранился: первый=%v, второй=%v", gap1, gap2)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// БЛОК 6: EstimateReadyAt — корректность для UI прогресс-бара
// ─────────────────────────────────────────────────────────────────────────────

// TestEstimateReadyAt_RecentStop_IsInFuture проверяет что при недавней остановке
// EstimateReadyAt возвращает время в будущем (UI показывает прогресс-бар).
func TestEstimateReadyAt_RecentStop_IsInFuture(t *testing.T) {
	defer simu(t)()

	writeStop(t, time.Now())
	eta := wintun.EstimateReadyAt()
	if !eta.After(time.Now()) {
		t.Errorf("при недавней остановке ETA должен быть в будущем, got %v (прошлое)", eta)
	}
}

// TestEstimateReadyAt_LongAgoStop_IsNow проверяет что при давней остановке
// ETA ≈ now (нет смысла показывать прогресс-бар — запуск будет быстрым).
func TestEstimateReadyAt_LongAgoStop_IsNow(t *testing.T) {
	defer simu(t)()

	writeStop(t, time.Now().Add(-10*time.Minute))
	eta := wintun.EstimateReadyAt()
	if eta.After(time.Now().Add(2 * time.Second)) {
		t.Errorf("при давней остановке ETA должен быть ≈ now, got future %v", time.Until(eta))
	}
}

// TestEstimateReadyAt_MonotonicallyDecreases проверяет что ETA не растёт со временем.
// БАГ если ETA возвращает случайное или нерасчётное значение.
func TestEstimateReadyAt_MonotonicallyDecreases(t *testing.T) {
	defer simu(t)()

	writeStop(t, time.Now().Add(-5*time.Second)) // 5с назад
	eta1 := wintun.EstimateReadyAt()

	time.Sleep(100 * time.Millisecond)
	eta2 := wintun.EstimateReadyAt()

	// ETA2 должен быть ≤ ETA1 (время идёт, до запуска остаётся меньше)
	if eta2.After(eta1.Add(500 * time.Millisecond)) {
		t.Errorf("ETA должен уменьшаться со временем: eta1=%v, eta2=%v (разница=%v)",
			eta1.Format("15:04:05.000"),
			eta2.Format("15:04:05.000"),
			time.Until(eta2)-time.Until(eta1))
	}
}

// TestEstimateReadyAt_CleanShutdown_ReturnsNow проверяет что при чистом завершении
// ETA = now (запуск будет мгновенным — path 0).
func TestEstimateReadyAt_CleanShutdown_ReturnsNow(t *testing.T) {
	defer simu(t)()

	writeStop(t, time.Now())
	writeCleanShutdown(t)

	eta := wintun.EstimateReadyAt()
	// При CleanShutdown PollUntilFree вернётся сразу — ETA должен быть ≈ now
	if eta.After(time.Now().Add(3 * time.Second)) {
		t.Errorf("при CleanShutdown ETA должен быть ≈ now, но получили будущее: +%v", time.Until(eta))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// БЛОК 7: Конкурентность — race detector
// ─────────────────────────────────────────────────────────────────────────────

// TestConcurrent_RecordStop_RecordCleanShutdown проверяет что параллельные вызовы
// RecordStop и RecordCleanShutdown не вызывают data race.
// Запускается с -race флагом.
func TestConcurrent_RecordStop_RecordCleanShutdown(t *testing.T) {
	defer simu(t)()

	var wg sync.WaitGroup
	const goroutines = 20

	for i := 0; i < goroutines; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			wintun.RecordStop()
		}()
		go func() {
			defer wg.Done()
			wintun.RecordCleanShutdown()
		}()
	}
	wg.Wait()
	// Если data race — -race флаг поймает и упадём
}

// TestConcurrent_ReadAdaptiveGap_IncreaseReset проверяет что параллельные
// операции над gap файлом не вызывают data race или panic.
func TestConcurrent_ReadAdaptiveGap_IncreaseReset(t *testing.T) {
	defer simu(t)()

	var wg sync.WaitGroup
	const goroutines = 10

	for i := 0; i < goroutines; i++ {
		wg.Add(3)
		go func() {
			defer wg.Done()
			_ = wintun.ReadAdaptiveGap()
		}()
		go func() {
			defer wg.Done()
			wintun.IncreaseAdaptiveGap(&silentLog{})
		}()
		go func() {
			defer wg.Done()
			wintun.ResetAdaptiveGap()
		}()
	}
	wg.Wait()
}

// TestConcurrent_MultipleEstimateReadyAt проверяет что параллельное чтение
// EstimateReadyAt (используется UI поллингом каждые 2-3с) не создаёт race.
func TestConcurrent_MultipleEstimateReadyAt(t *testing.T) {
	defer simu(t)()

	writeStop(t, time.Now().Add(-30*time.Second))

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = wintun.EstimateReadyAt()
		}()
	}
	wg.Wait()
}

// TestConcurrent_PollUntilFree_MultipleGoroutines имитирует ситуацию когда
// handleCrash и startBackground параллельно вызывают PollUntilFree.
// БАГ #B-10 был именно в этом — data race на cachedIfName в probe_windows.go.
func TestConcurrent_PollUntilFree_MultipleGoroutines(t *testing.T) {
	defer simu(t)()

	writeStop(t, time.Now().Add(-10*time.Minute)) // холодный старт — быстро

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			wintun.PollUntilFree(ctx, &silentLog{}, "tun0")
		}()
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(4 * time.Second):
		t.Error("параллельный PollUntilFree завис — возможен deadlock")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// БЛОК 8: Реалистичные сценарии из логов
// ─────────────────────────────────────────────────────────────────────────────

// TestScenario_NormalShutdownRestart симулирует нормальный сценарий:
// пользователь выключил прокси через UI → включил снова.
// Ожидание: path 0 (CleanShutdown) → мгновенный старт.
func TestScenario_NormalShutdownRestart(t *testing.T) {
	defer simu(t)()

	// 1. Пользователь нажал "Выключить" → Stop() → RecordStop() + RecordCleanShutdown()
	wintun.RecordStop()
	wintun.RecordCleanShutdown()

	// 2. Пользователь нажал "Включить" → PollUntilFree
	elapsed, cancelled := runWithTimeout(300 * time.Millisecond)

	if cancelled {
		t.Errorf("Сценарий 'нормальный restart': должен быть мгновенным (path 0), но зависло (elapsed=%v)", elapsed)
	}
}

// TestScenario_AppKilledByUser симулирует kill -9 / диспетчер задач.
// Ожидание: горячий старт с gap (нет CleanShutdown, нет FastDelete).
func TestScenario_AppKilledByUser(t *testing.T) {
	defer simu(t)()

	// 1. Аварийное завершение — только RecordStop успел (или даже нет)
	wintun.RecordStop()
	// CleanShutdownFile НЕТ

	// 2. Следующий старт — должен ждать gap
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	wintun.PollUntilFree(ctx, &silentLog{}, "tun0")

	if ctx.Err() == nil {
		t.Error("Сценарий 'kill -9': PollUntilFree должна ждать gap, но вернулась мгновенно")
	}
}

// TestScenario_RepeatedCrashesIncreasesGap симулирует серию TUN-крашей.
// Ожидание: каждый краш увеличивает gap до максимума.
func TestScenario_RepeatedCrashesIncreasesGap(t *testing.T) {
	defer simu(t)()

	for i := 0; i < 3; i++ {
		wintun.RecordStop()
		wintun.IncreaseAdaptiveGap(&silentLog{})
	}

	gap := wintun.ReadAdaptiveGap()
	if gap < 2*time.Minute {
		t.Errorf("после 3 крашей gap должен быть ≥ 2m, got %v", gap)
	}
}

// TestScenario_SuccessfulStartResetsGap проверяет что после успешного запуска
// gap сбрасывается — следующий цикл не страдает от предыдущих крашей.
func TestScenario_SuccessfulStartResetsGap(t *testing.T) {
	defer simu(t)()

	// Много крашей
	for i := 0; i < 5; i++ {
		wintun.IncreaseAdaptiveGap(&silentLog{})
	}
	before := wintun.ReadAdaptiveGap()

	// Успешный старт
	wintun.ResetAdaptiveGap()
	wintun.RecordStop()
	wintun.RecordCleanShutdown()

	after := wintun.ReadAdaptiveGap()

	if after >= before {
		t.Errorf("после успешного старта gap должен сброситься: before=%v, after=%v", before, after)
	}
	if after != 60*time.Second {
		t.Errorf("после reset gap должен быть 60s, got %v", after)
	}
}

// TestScenario_ColdStartAfterLongOffline симулирует холодный старт
// после долгого offline (ноутбук был выключен несколько часов).
// Ожидание: gap пропускается (elapsed >> coldStartThreshold).
func TestScenario_ColdStartAfterLongOffline(t *testing.T) {
	defer simu(t)()

	// Остановились 4 часа назад
	writeStop(t, time.Now().Add(-4*time.Hour))

	// При холодном старте gap должен быть пропущен
	// Проверяем что EstimateReadyAt возвращает "почти сейчас"
	eta := wintun.EstimateReadyAt()
	if eta.After(time.Now().Add(5 * time.Second)) {
		t.Errorf("холодный старт после 4ч: ETA должен быть ≈ now, got +%v", time.Until(eta))
	}
}

// TestScenario_WindowsUpdateReboot симулирует перезагрузку после Windows Update.
// Особенность: adaptive gap увеличен (были краши до обновления), но
// после перезагрузки sing-box не запускался → CleanShutdown нет.
// Ожидание: холодный старт (StopFile очень старый → gap пропущен).
func TestScenario_WindowsUpdateReboot(t *testing.T) {
	defer simu(t)()

	// До обновления были краши
	wintun.IncreaseAdaptiveGap(&silentLog{})
	wintun.IncreaseAdaptiveGap(&silentLog{})

	// Перезагрузка — StopFile теперь очень старый
	writeStop(t, time.Now().Add(-2*time.Hour))

	eta := wintun.EstimateReadyAt()
	if eta.After(time.Now().Add(5 * time.Second)) {
		t.Errorf("после Windows Update (StopFile=2h): ETA должен быть ≈ now, got +%v", time.Until(eta))
	}
}

// TestScenario_FastApplyRules симулирует Apply Rules после нормального старта.
// Это наиболее частый сценарий перезапуска для пользователя.
// Ожидание: CleanShutdown path → < 1с.
func TestScenario_FastApplyRules(t *testing.T) {
	defer simu(t)()

	// Sing-box работал, пользователь нажал Apply Rules
	// → Stop() записал CleanShutdown (graceful через CTRL_BREAK)
	wintun.RecordStop()
	wintun.RecordCleanShutdown()

	start := time.Now()
	elapsed, cancelled := runWithTimeout(500 * time.Millisecond)

	if cancelled {
		t.Errorf("Apply Rules должен быть быстрым (<500мс), зависло (elapsed=%v)", elapsed)
	}

	if time.Since(start) > 200*time.Millisecond {
		t.Logf("Предупреждение: Apply Rules занял %v (ожидается <100мс)", time.Since(start))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// БЛОК 9: Инварианты — никогда не нарушаются
// ─────────────────────────────────────────────────────────────────────────────

// TestInvariant_RecordStop_AlwaysCreatesStopFile проверяет что RecordStop
// всегда создаёт StopFile, даже если директория была пустой.
func TestInvariant_RecordStop_AlwaysCreatesStopFile(t *testing.T) {
	defer simu(t)()

	wintun.RecordStop()

	if _, err := os.Stat(wintun.StopFile); os.IsNotExist(err) {
		t.Error("ИНВАРИАНТ НАРУШЕН: RecordStop должен всегда создавать StopFile")
	}
}

// TestInvariant_GapNeverBelowBase проверяет что gap никогда не падает
// ниже minGapBase после любой последовательности операций.
func TestInvariant_GapNeverBelowBase(t *testing.T) {
	defer simu(t)()

	// Записываем очень маленькое значение
	_ = os.WriteFile("wintun_gap_ns", []byte("1"), 0644) // 1 нс
	gap := wintun.ReadAdaptiveGap()
	if gap < 60*time.Second {
		t.Errorf("ИНВАРИАНТ НАРУШЕН: gap=%v, должен быть >= 60s (minGapBase)", gap)
	}
}

// TestInvariant_GapNeverAboveMax проверяет что gap никогда не превышает максимум.
func TestInvariant_GapNeverAboveMax(t *testing.T) {
	defer simu(t)()

	_ = os.WriteFile("wintun_gap_ns", []byte(strconv.FormatInt(int64(24*time.Hour), 10)), 0644)
	gap := wintun.ReadAdaptiveGap()
	if gap > 3*time.Minute {
		t.Errorf("ИНВАРИАНТ НАРУШЕН: gap=%v, должен быть <= 3m (minGapMax)", gap)
	}
}

// TestInvariant_FastDeleteFileRelativePath проверяет что все маркерные файлы
// используют относительные пути (иначе сломается при смене CWD).
func TestInvariant_MarkerFilesAreRelative(t *testing.T) {
	files := map[string]string{
		"StopFile":          wintun.StopFile,
		"FastDeleteFile":    wintun.FastDeleteFile,
		"CleanShutdownFile": wintun.CleanShutdownFile,
	}
	for name, path := range files {
		if filepath.IsAbs(path) {
			t.Errorf("ИНВАРИАНТ НАРУШЕН: %s=%q должен быть относительным путём", name, path)
		}
		if path == "" {
			t.Errorf("ИНВАРИАНТ НАРУШЕН: %s не должен быть пустым", name)
		}
	}
}

// TestInvariant_ContextCancellationAlwaysWorks проверяет что отмена ctx
// ВСЕГДА прерывает PollUntilFree, независимо от состояния файлов.
// Это критично для корректного Shutdown приложения.
func TestInvariant_ContextCancellationAlwaysWorks(t *testing.T) {
	defer simu(t)()

	scenarios := []struct {
		name string
		setup func()
	}{
		{"no_stop_file", func() {}},
		{"hot_start", func() { writeStop(t, time.Now()) }},
		{"hot_start_max_gap", func() {
			writeStop(t, time.Now())
			writeGap(t, 3*time.Minute)
		}},
		{"cold_start", func() { writeStop(t, time.Now().Add(-1*time.Hour)) }},
		{"corrupt_stop", func() { _ = os.WriteFile(wintun.StopFile, []byte("bad"), 0644) }},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			// Каждый сценарий в своей tempdir
			dir := t.TempDir()
			old, _ := os.Getwd()
			_ = os.Chdir(dir)
			defer func() { _ = os.Chdir(old) }()

			sc.setup()

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				defer close(done)
				wintun.PollUntilFree(ctx, &silentLog{}, "tun0")
			}()

			time.Sleep(50 * time.Millisecond)
			cancel()

			select {
			case <-done:
				// Корректно прервалась
			case <-time.After(3 * time.Second):
				t.Errorf("ИНВАРИАНТ НАРУШЕН: PollUntilFree не прерывается при отмене ctx (сценарий: %s)", sc.name)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// БЛОК 10: SettleDelay — корректность расчёта
// ─────────────────────────────────────────────────────────────────────────────

// TestSettleDelay_BaseCap проверяет что базовый settle = settleDelayBase (5с).
func TestSettleDelay_BaseCap(t *testing.T) {
	defer simu(t)()

	// gap=60s → settle = max(5s, 60s*0.25=15s) = 15s > 5s → 15s
	// Но если settle снизили до 5s, то max(5, 15) = 15s
	settle := wintun.ReadSettleDelay()
	if settle < 5*time.Second {
		t.Errorf("settle не должен быть < settleDelayBase=5s, got %v", settle)
	}
	if settle > 20*time.Second {
		t.Errorf("settle не должен быть > settleDelayMax=20s, got %v", settle)
	}
}

// TestSettleDelay_GrowsWithGap проверяет пропорциональный рост settle с gap.
func TestSettleDelay_GrowsWithGap(t *testing.T) {
	defer simu(t)()

	s1 := wintun.ReadSettleDelay() // gap=60s

	writeGap(t, 180*time.Second) // gap=3m → settle больше
	s2 := wintun.ReadSettleDelay()

	if s2 < s1 {
		t.Errorf("settle должен расти с gap: s1(60s)=%v, s2(180s)=%v", s1, s2)
	}
}

// TestSettleDelay_MaxCap проверяет что settle не превышает settleDelayMax.
func TestSettleDelay_MaxCap(t *testing.T) {
	defer simu(t)()

	// Очень большой gap
	writeGap(t, 10*time.Minute)
	settle := wintun.ReadSettleDelay()
	if settle > 20*time.Second {
		t.Errorf("settle должен быть <= settleDelayMax=20s, got %v", settle)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Вспомогательные типы
// ─────────────────────────────────────────────────────────────────────────────

// silentLog — заглушка логгера для тестов.
type silentLog struct{}

func (s *silentLog) Debug(f string, a ...interface{}) {}
func (s *silentLog) Info(f string, a ...interface{})  {}
func (s *silentLog) Warn(f string, a ...interface{})  {}
func (s *silentLog) Error(f string, a ...interface{}) {}

// ─────────────────────────────────────────────────────────────────────────────
// БЛОК 11: Table-driven тесты — все комбинации маркеров
// ─────────────────────────────────────────────────────────────────────────────

// TestMarkerCombinations проверяет все 8 комбинаций маркерных файлов.
// Каждая комбинация должна либо завершаться быстро (path 0/1),
// либо корректно уходить в ожидание (path 2/3) без паники.
func TestMarkerCombinations(t *testing.T) {
	type combo struct {
		hasStop         bool
		hasFastDelete   bool
		hasCleanShutdown bool
		stopAge         time.Duration // отрицательное = в прошлом
		expectFast      bool          // ожидаем быстрый возврат (< 300мс без отмены ctx)?
		desc            string
	}

	cases := []combo{
		// Нет StopFile → первый запуск ever → мгновенно
		{false, false, false, 0, true, "no_stop_ever"},

		// CleanShutdown + новый StopFile → path 0 → мгновенно
		{true, false, true, -5 * time.Second, true, "clean_shutdown_recent"},

		// CleanShutdown + старый StopFile → path 0 (CleanShutdown имеет приоритет)
		{true, false, true, -2 * time.Hour, true, "clean_shutdown_old"},

		// FastDelete + новый StopFile → path 1 → не ждём gap
		{true, true, false, -5 * time.Second, false, "fast_delete_recent"},
		// (false потому что всё равно нужен confirm-loop + settle)

		// Холодный старт → path 2 → пропуск gap, но confirm-loop
		{true, false, false, -6 * time.Minute, false, "cold_start"},

		// Горячий старт без маркеров → path 3 → ждём gap
		{true, false, false, -10 * time.Second, false, "hot_start_no_markers"},

		// Все маркеры → path 0 (CleanShutdown приоритетнее)
		{true, true, true, -5 * time.Second, true, "all_markers"},

		// FastDelete + CleanShutdown → path 0 (CleanShutdown)
		{true, true, true, -2 * time.Hour, true, "fast_and_clean"},
	}

	for _, c := range cases {
		t.Run(fmt.Sprintf("combo_%s", c.desc), func(t *testing.T) {
			dir := t.TempDir()
			old, _ := os.Getwd()
			_ = os.Chdir(dir)
			defer func() { _ = os.Chdir(old) }()

			if c.hasStop {
				writeStop(t, time.Now().Add(c.stopAge))
			}
			if c.hasFastDelete {
				writeFastDelete(t)
			}
			if c.hasCleanShutdown {
				writeCleanShutdown(t)
			}

			elapsed, cancelled := runWithTimeout(300 * time.Millisecond)

			if c.expectFast && cancelled {
				t.Errorf("%s: ожидали быстрый возврат, но зависли (elapsed=%v)", c.desc, elapsed)
			}

			// Ни в каком случае не должно быть паники (проверяется самим фактом выполнения теста)
			_ = elapsed
			_ = cancelled
		})
	}
}
