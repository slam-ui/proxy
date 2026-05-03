package trafficstats

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func resetStatsForTest(tb testing.TB) {
	tb.Helper()
	oldDown := sessionDown.Swap(0)
	oldUp := sessionUp.Swap(0)
	statsCacheMu.Lock()
	statsCache = cachedStats{}
	statsCacheMu.Unlock()
	oldWD, err := os.Getwd()
	if err != nil {
		tb.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(tb.TempDir()); err != nil {
		tb.Fatalf("Chdir: %v", err)
	}
	tb.Cleanup(func() {
		sessionDown.Store(oldDown)
		sessionUp.Store(oldUp)
		statsCacheMu.Lock()
		statsCache = cachedStats{}
		statsCacheMu.Unlock()
		_ = os.Chdir(oldWD)
	})
}

func TestSaveToFileRestoresSessionCountersOnWriteError(t *testing.T) {
	resetStatsForTest(t)

	AddSession(123, 45)
	if err := SaveToFile(); err == nil {
		t.Fatal("SaveToFile succeeded without data directory, want error")
	}

	current := Current()
	if current.SessionDownloadBytes != 123 || current.SessionUploadBytes != 45 {
		t.Fatalf("session counters = %d/%d, want 123/45", current.SessionDownloadBytes, current.SessionUploadBytes)
	}
}

func TestSaveToFilePersistsSessionTotals(t *testing.T) {
	resetStatsForTest(t)
	if err := os.MkdirAll("data", 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	AddSession(100, 25)
	if err := SaveToFile(); err != nil {
		t.Fatalf("SaveToFile: %v", err)
	}

	current := Current()
	if current.TotalDownloadBytes != 100 || current.TotalUploadBytes != 25 {
		t.Fatalf("totals = %d/%d, want 100/25", current.TotalDownloadBytes, current.TotalUploadBytes)
	}
	if current.SessionDownloadBytes != 0 || current.SessionUploadBytes != 0 {
		t.Fatalf("session counters = %d/%d, want 0/0", current.SessionDownloadBytes, current.SessionUploadBytes)
	}
}

func TestCurrentInvalidatesCacheWhenStatsFileChanges(t *testing.T) {
	resetStatsForTest(t)
	if err := os.MkdirAll("data", 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeStatsFile(t, Stats{TotalDownloadBytes: 10, TotalUploadBytes: 2}, time.Now())

	first := Current()
	if first.TotalDownloadBytes != 10 || first.TotalUploadBytes != 2 {
		t.Fatalf("first totals = %d/%d, want 10/2", first.TotalDownloadBytes, first.TotalUploadBytes)
	}

	nextTime := time.Now().Add(2 * time.Second)
	writeStatsFile(t, Stats{TotalDownloadBytes: 20, TotalUploadBytes: 4}, nextTime)

	second := Current()
	if second.TotalDownloadBytes != 20 || second.TotalUploadBytes != 4 {
		t.Fatalf("second totals = %d/%d, want 20/4", second.TotalDownloadBytes, second.TotalUploadBytes)
	}
}

func BenchmarkCurrentCached(b *testing.B) {
	resetStatsForTest(b)
	if err := os.MkdirAll("data", 0755); err != nil {
		b.Fatalf("MkdirAll: %v", err)
	}
	writeStatsFile(b, Stats{TotalDownloadBytes: 100, TotalUploadBytes: 25}, time.Now())
	_ = Current()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Current()
	}
}

func writeStatsFile(tb testing.TB, s Stats, modTime time.Time) {
	tb.Helper()
	data, err := json.Marshal(s)
	if err != nil {
		tb.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(statsFile, data, 0644); err != nil {
		tb.Fatalf("WriteFile: %v", err)
	}
	if err := os.Chtimes(statsFile, modTime, modTime); err != nil {
		tb.Fatalf("Chtimes: %v", err)
	}
}
