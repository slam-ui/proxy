package trafficstats

import (
	"os"
	"testing"
)

func resetStatsForTest(t *testing.T) {
	t.Helper()
	oldDown := sessionDown.Swap(0)
	oldUp := sessionUp.Swap(0)
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() {
		sessionDown.Store(oldDown)
		sessionUp.Store(oldUp)
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
