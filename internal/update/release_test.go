package update

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCompareReleaseUpdateAvailable(t *testing.T) {
	rel := validRelease(t, "1.2.0", []byte("payload"))
	res, err := compareRelease("1.1.0", rel, time.Unix(1, 0))
	if err != nil {
		t.Fatalf("compareRelease: %v", err)
	}
	if !res.UpdateAvailable || res.ManualUpdateRequired {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestCompareReleaseRequiresManualUpdate(t *testing.T) {
	rel := validRelease(t, "1.2.0", []byte("payload"))
	rel.MinVersionForDirectUpdate = "1.1.0"
	res, err := compareRelease("1.0.0", rel, time.Unix(1, 0))
	if err != nil {
		t.Fatalf("compareRelease: %v", err)
	}
	if !res.ManualUpdateRequired || res.UpdateAvailable {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestReleaseRejectsHTTPDownloadURL(t *testing.T) {
	rel := validRelease(t, "1.2.0", []byte("payload"))
	rel.DownloadURL = "http://example.com/update.exe"
	if err := rel.Validate(); err == nil {
		t.Fatal("Validate accepted http download_url")
	}
}

func TestDownloadVerifiesSHA256AndSize(t *testing.T) {
	body := []byte("new exe")
	rel := validRelease(t, "1.2.0", body)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	rel.DownloadURL = srv.URL + "/SafeSky.exe"
	up, err := New(Config{
		BaseURL:        "https://updates.example.test",
		CurrentVersion: "1.1.0",
		HTTPClient:     srv.Client(),
		TempDir:        t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := up.Download(t.Context(), rel)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	data, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(data) != string(body) {
		t.Fatalf("downloaded body = %q", data)
	}
}

func TestDownloadDeletesTempFileOnHashMismatch(t *testing.T) {
	body := []byte("new exe")
	rel := validRelease(t, "1.2.0", body)
	rel.SHA256 = strings.Repeat("0", 64)
	tmpDir := t.TempDir()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	rel.DownloadURL = srv.URL + "/SafeSky.exe"
	up, err := New(Config{
		BaseURL:        "https://updates.example.test",
		CurrentVersion: "1.1.0",
		HTTPClient:     srv.Client(),
		TempDir:        tmpDir,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := up.Download(t.Context(), rel); err == nil {
		t.Fatal("Download succeeded with wrong hash")
	}
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}

func TestCheckLatestFetchesChannelVersionJSON(t *testing.T) {
	body := []byte("new exe")
	rel := validRelease(t, "1.2.0", body)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/updates/version.beta.json" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		rel.DownloadURL = "https://download.example.test/SafeSky.exe"
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"channel":"beta","version":"1.2.0","released_at":"2026-05-01T12:00:00Z","min_version_for_direct_update":"1.0.0","download_url":"https://download.example.test/SafeSky.exe","sha256":"` + rel.SHA256 + `","size_bytes":` + strconv.FormatInt(rel.SizeBytes, 10) + `,"critical":false}`))
	}))
	defer srv.Close()
	up, err := New(Config{
		BaseURL:        srv.URL + "/updates",
		Channel:        ChannelBeta,
		CurrentVersion: "1.1.0",
		HTTPClient:     srv.Client(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res, err := up.CheckLatest(t.Context())
	if err != nil {
		t.Fatalf("CheckLatest: %v", err)
	}
	if !res.UpdateAvailable {
		t.Fatalf("expected update: %+v", res)
	}
}

func TestStateRoundTripAndMarkVerified(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update_state.json")
	if err := SaveState(path, State{PendingPath: "new.exe", Verified: false}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	if err := MarkVerified(path); err != nil {
		t.Fatalf("MarkVerified: %v", err)
	}
	st, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !st.Verified || st.PendingPath != "" {
		t.Fatalf("unexpected state: %+v", st)
	}
}

func validRelease(t *testing.T, version string, body []byte) *Release {
	t.Helper()
	sum := sha256.Sum256(body)
	return &Release{
		Channel:                   ChannelStable,
		Version:                   version,
		ReleasedAt:                time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
		MinVersionForDirectUpdate: "1.0.0",
		DownloadURL:               "https://download.example.test/SafeSky.exe",
		SHA256:                    hex.EncodeToString(sum[:]),
		SizeBytes:                 int64(len(body)),
	}
}
