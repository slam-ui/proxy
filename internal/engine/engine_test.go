//go:build windows
// +build windows

package engine

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── NeedsDownload Tests ────────────────────────────────────────────────────────

func TestNeedsDownload_ReturnsTrue_WhenFileNotExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.exe")

	if !NeedsDownload(path) {
		t.Error("Expected NeedsDownload=true for nonexistent file")
	}
}

func TestNeedsDownload_ReturnsTrue_WhenFileEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.exe")

	if err := os.WriteFile(path, []byte{}, 0755); err != nil {
		t.Fatalf("Failed to create empty file: %v", err)
	}

	if !NeedsDownload(path) {
		t.Error("Expected NeedsDownload=true for empty file")
	}
}

func TestNeedsDownload_ReturnsFalse_WhenFileExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing.exe")

	// File must be >= MinValidBinarySize (1MB) to be considered valid
	if err := os.WriteFile(path, make([]byte, 1024*1024), 0755); err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	if NeedsDownload(path) {
		t.Error("Expected NeedsDownload=false for existing non-empty file")
	}
}

func TestNeedsDownload_ReturnsFalse_WhenFileHasContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sing-box.exe")

	// Create file with realistic size
	content := make([]byte, 1024*1024) // 1MB
	if err := os.WriteFile(path, content, 0755); err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	if NeedsDownload(path) {
		t.Error("Expected NeedsDownload=false for file with content")
	}
}

// ── fmtBytes Tests ──────────────────────────────────────────────────────────────

func TestFmtBytes_Negative(t *testing.T) {
	if got := fmtBytes(-1); got != "?" {
		t.Errorf("fmtBytes(-1) = %q, want ?", got)
	}
}

func TestFmtBytes_Zero(t *testing.T) {
	if got := fmtBytes(0); got != "0B" {
		t.Errorf("fmtBytes(0) = %q, want 0B", got)
	}
}

func TestFmtBytes_Bytes(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{1, "1B"},
		{512, "512B"},
		{1023, "1023B"},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("%d", tc.input), func(t *testing.T) {
			if got := fmtBytes(tc.input); got != tc.expected {
				t.Errorf("fmtBytes(%d) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestFmtBytes_Kilobytes(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{1024, "1.0KB"},
		{1536, "1.5KB"},
		{10240, "10.0KB"},
		{1024*1024 - 1, "1024.0KB"},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("%d", tc.input), func(t *testing.T) {
			if got := fmtBytes(tc.input); got != tc.expected {
				t.Errorf("fmtBytes(%d) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestFmtBytes_Megabytes(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{1024 * 1024, "1.0MB"},
		{10 * 1024 * 1024, "10.0MB"},
		{15 * 1024 * 1024, "15.0MB"},
		{1536 * 1024, "1.5MB"},
	}

	for _, tc := range tests {
		t.Run(tc.expected, func(t *testing.T) {
			if got := fmtBytes(tc.input); got != tc.expected {
				t.Errorf("fmtBytes(%d) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

// ── max64 Tests ─────────────────────────────────────────────────────────────────

func TestMax64_FirstLarger(t *testing.T) {
	if got := max64(10, 5); got != 10 {
		t.Errorf("max64(10, 5) = %d, want 10", got)
	}
}

func TestMax64_SecondLarger(t *testing.T) {
	if got := max64(5, 10); got != 10 {
		t.Errorf("max64(5, 10) = %d, want 10", got)
	}
}

func TestMax64_Equal(t *testing.T) {
	if got := max64(7, 7); got != 7 {
		t.Errorf("max64(7, 7) = %d, want 7", got)
	}
}

func TestMax64_Negative(t *testing.T) {
	if got := max64(-5, -10); got != -5 {
		t.Errorf("max64(-5, -10) = %d, want -5", got)
	}
}

func TestMax64_ZeroAndNegative(t *testing.T) {
	if got := max64(0, -1); got != 0 {
		t.Errorf("max64(0, -1) = %d, want 0", got)
	}
}

// ── verifyChecksum Tests ────────────────────────────────────────────────────────

func TestVerifyChecksum_ReturnsNil_WhenEmpty(t *testing.T) {
	data := []byte("some data")
	if err := verifyChecksum(data, ""); err != nil {
		t.Errorf("verifyChecksum with empty expected should return nil, got: %v", err)
	}
}

func TestVerifyChecksum_ReturnsNil_WhenMatch(t *testing.T) {
	data := []byte("test data for checksum")
	sum := sha256.Sum256(data)
	expected := hex.EncodeToString(sum[:])

	if err := verifyChecksum(data, expected); err != nil {
		t.Errorf("verifyChecksum with matching sum should return nil, got: %v", err)
	}
}

func TestVerifyChecksum_ReturnsError_WhenMismatch(t *testing.T) {
	data := []byte("test data")
	wrongSum := strings.Repeat("a", 64)

	err := verifyChecksum(data, wrongSum)
	if err == nil {
		t.Error("verifyChecksum with wrong sum should return error")
	}

	if !strings.Contains(err.Error(), "контрольная сумма не совпадает") {
		t.Errorf("Error message should mention checksum mismatch, got: %v", err)
	}
}

func TestVerifyChecksum_CaseInsensitive(t *testing.T) {
	data := []byte("test data for case test")
	sum := sha256.Sum256(data)
	expected := hex.EncodeToString(sum[:])

	// Test uppercase
	if err := verifyChecksum(data, strings.ToUpper(expected)); err != nil {
		t.Errorf("verifyChecksum should accept uppercase hex, got: %v", err)
	}
}

// ── extractExeFromZip Tests ─────────────────────────────────────────────────────

func TestExtractExeFromZip_ReturnsError_WhenInvalidZip(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "out.exe")

	err := extractExeFromZip([]byte("not a zip"), dest)
	if err == nil {
		t.Error("Expected error for invalid zip data")
	}
}

func TestExtractExeFromZip_ReturnsError_WhenNoExe(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "out.exe")

	// Create zip without sing-box.exe
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	fw, _ := w.Create("readme.txt")
	fw.Write([]byte("readme content"))
	w.Close()

	err := extractExeFromZip(buf.Bytes(), dest)
	if err == nil {
		t.Error("Expected error when zip has no sing-box.exe")
	}
	if !strings.Contains(err.Error(), "не найден в архиве") {
		t.Errorf("Error should mention exe not found, got: %v", err)
	}
}

func TestExtractExeFromZip_ExtractsExe(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "sing-box.exe")

	// Create zip with sing-box.exe
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	fw, _ := w.Create("sing-box.exe")
	fw.Write([]byte("fake exe content"))
	w.Close()

	err := extractExeFromZip(buf.Bytes(), dest)
	if err != nil {
		t.Fatalf("extractExeFromZip failed: %v", err)
	}

	// Verify file exists and has correct content
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("Failed to read extracted file: %v", err)
	}
	if string(data) != "fake exe content" {
		t.Errorf("Extracted content = %q, want %q", string(data), "fake exe content")
	}
}

func TestExtractExeFromZip_ExtractsFromSubdirectory(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "sing-box.exe")

	// Create zip with sing-box.exe in subdirectory
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	fw, _ := w.Create("sing-box-1.0.0/sing-box.exe")
	fw.Write([]byte("exe in subdir"))
	w.Close()

	err := extractExeFromZip(buf.Bytes(), dest)
	if err != nil {
		t.Fatalf("extractExeFromZip failed: %v", err)
	}

	data, _ := os.ReadFile(dest)
	if string(data) != "exe in subdir" {
		t.Errorf("Extracted content = %q, want %q", string(data), "exe in subdir")
	}
}

// ── copyFile Tests ──────────────────────────────────────────────────────────────

func TestCopyFile_CopiesContent(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")

	if err := os.WriteFile(src, []byte("source content"), 0644); err != nil {
		t.Fatalf("Failed to create source: %v", err)
	}

	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile failed: %v", err)
	}

	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("Failed to read destination: %v", err)
	}

	if string(data) != "source content" {
		t.Errorf("Copied content = %q, want %q", string(data), "source content")
	}
}

func TestCopyFile_ReturnsError_WhenSourceNotExists(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "dst.txt")

	err := copyFile(filepath.Join(dir, "nonexistent"), dst)
	if err == nil {
		t.Error("Expected error when source doesn't exist")
	}
}

func TestCopyFile_OverwritesDestination(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")

	if err := os.WriteFile(src, []byte("new content"), 0644); err != nil {
		t.Fatalf("Failed to create source: %v", err)
	}
	if err := os.WriteFile(dst, []byte("old content"), 0644); err != nil {
		t.Fatalf("Failed to create destination: %v", err)
	}

	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile failed: %v", err)
	}

	data, _ := os.ReadFile(dst)
	if string(data) != "new content" {
		t.Errorf("Copied content = %q, want %q", string(data), "new content")
	}
}

// ── fetchLatestAsset Tests ──────────────────────────────────────────────────────

func TestFetchLatestAsset_ReturnsError_OnHTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	// Override githubAPI for test
	oldAPI := githubAPI
	githubAPI = ts.URL
	defer func() { githubAPI = oldAPI }()

	_, _, err := fetchLatestAsset(context.Background())
	if err == nil {
		t.Error("Expected error on HTTP 500")
	}
}

func TestFetchLatestAsset_ReturnsError_OnInvalidJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("invalid json"))
	}))
	defer ts.Close()

	oldAPI := githubAPI
	githubAPI = ts.URL
	defer func() { githubAPI = oldAPI }()

	_, _, err := fetchLatestAsset(context.Background())
	if err == nil {
		t.Error("Expected error on invalid JSON")
	}
}

func TestFetchLatestAsset_ReturnsError_OnNoWindowsAsset(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		release := githubRelease{
			TagName: "v1.0.0",
			Assets: []githubAsset{
				{Name: "sing-box-1.0.0-linux-amd64.zip", DownloadURL: "http://example.com/linux.zip"},
			},
		}
		json.NewEncoder(w).Encode(release)
	}))
	defer ts.Close()

	oldAPI := githubAPI
	githubAPI = ts.URL
	defer func() { githubAPI = oldAPI }()

	_, _, err := fetchLatestAsset(context.Background())
	if err == nil {
		t.Error("Expected error when no windows-amd64 asset")
	}
}

func TestFetchLatestAsset_ReturnsAsset_OnSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		release := githubRelease{
			TagName: "v1.13.7",
			Assets: []githubAsset{
				{Name: "sing-box-1.13.7-windows-amd64.zip", DownloadURL: "http://example.com/windows.zip", Size: 1024},
			},
		}
		json.NewEncoder(w).Encode(release)
	}))
	defer ts.Close()

	oldAPI := githubAPI
	githubAPI = ts.URL
	defer func() { githubAPI = oldAPI }()

	asset, version, err := fetchLatestAsset(context.Background())
	if err != nil {
		t.Fatalf("fetchLatestAsset failed: %v", err)
	}

	if version != "v1.13.7" {
		t.Errorf("Version = %q, want v1.13.7", version)
	}
	if asset.Name != "sing-box-1.13.7-windows-amd64.zip" {
		t.Errorf("Asset name = %q, want sing-box-1.13.7-windows-amd64.zip", asset.Name)
	}
}

func TestFetchLatestAsset_FetchesChecksum(t *testing.T) {
	// Create mock server for both release and checksum
	expectedSum := strings.Repeat("a", 64)

	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			w.Write([]byte(expectedSum + "  sing-box-1.13.7-windows-amd64.zip"))
			return
		}

		release := githubRelease{
			TagName: "v1.13.7",
			Assets: []githubAsset{
				{Name: "sing-box-1.13.7-windows-amd64.zip", DownloadURL: "http://example.com/windows.zip"},
				{Name: "sing-box-1.13.7-windows-amd64.zip.sha256", DownloadURL: ts.URL + "/checksum.sha256"},
			},
		}
		json.NewEncoder(w).Encode(release)
	}))
	defer ts.Close()

	oldAPI := githubAPI
	githubAPI = ts.URL
	defer func() { githubAPI = oldAPI }()

	asset, _, err := fetchLatestAsset(context.Background())
	if err != nil {
		t.Fatalf("fetchLatestAsset failed: %v", err)
	}

	if asset.Checksum != expectedSum {
		t.Errorf("Checksum = %q, want %q", asset.Checksum, expectedSum)
	}
}

// ── fetchLatestAssetFallback Tests ─────────────────────────────────────────────

// BUG FIX: fallback больше не делает HTTP запрос — конструирует URL из pinnedVersion.
// Проверяем что URL корректен и версия совпадает с pinnedVersion.
func TestFetchLatestAssetFallback_ConstructsURLFromPinnedVersion(t *testing.T) {
	noClient := &http.Client{Timeout: time.Second, Transport: http.DefaultTransport}
	asset, version, err := fetchLatestAssetFallback(context.Background(), noClient)
	if err != nil {
		t.Fatalf("fetchLatestAssetFallback вернул ошибку: %v", err)
	}
	if version != pinnedVersion {
		t.Errorf("version = %q, want %q", version, pinnedVersion)
	}
	ver := strings.TrimPrefix(pinnedVersion, "v")
	wantName := fmt.Sprintf("sing-box-%s-windows-amd64.zip", ver)
	if asset.Name != wantName {
		t.Errorf("asset.Name = %q, want %q", asset.Name, wantName)
	}
	wantURL := fmt.Sprintf("https://github.com/SagerNet/sing-box/releases/download/%s/%s", pinnedVersion, wantName)
	if asset.DownloadURL != wantURL {
		t.Errorf("asset.DownloadURL = %q, want %q", asset.DownloadURL, wantURL)
	}
}

// Проверяем что после смены pinnedVersion githubAPI тоже обновляется.
func TestPinnedVersion_GithubAPIContainsTag(t *testing.T) {
	if !strings.Contains(githubAPI, pinnedVersion) {
		t.Errorf("githubAPI %q не содержит pinnedVersion %q", githubAPI, pinnedVersion)
	}
}

// ── downloadWithProgress Tests ──────────────────────────────────────────────────

func TestDownloadWithProgress_ReturnsError_OnHTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	_, err := downloadWithProgress(context.Background(), ts.URL, nil)
	if err == nil {
		t.Error("Expected error on HTTP 404")
	}
}

func TestDownloadWithProgress_ReturnsData_OnSuccess(t *testing.T) {
	content := []byte("downloaded content")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer ts.Close()

	data, err := downloadWithProgress(context.Background(), ts.URL, nil)
	if err != nil {
		t.Fatalf("downloadWithProgress failed: %v", err)
	}

	if !bytes.Equal(data, content) {
		t.Errorf("Downloaded data = %q, want %q", string(data), string(content))
	}
}

func TestDownloadWithProgress_ReportsProgress(t *testing.T) {
	content := make([]byte, 500*1024) // 500KB
	for i := range content {
		content[i] = byte(i % 256)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		w.Write(content)
	}))
	defer ts.Close()

	var progressCalls []struct{ downloaded, total int64 }
	_, err := downloadWithProgress(context.Background(), ts.URL, func(d, t int64) {
		progressCalls = append(progressCalls, struct{ downloaded, total int64 }{d, t})
	})

	if err != nil {
		t.Fatalf("downloadWithProgress failed: %v", err)
	}

	if len(progressCalls) == 0 {
		t.Error("Expected progress callbacks")
	}

	// Verify total is correct
	for _, pc := range progressCalls {
		if pc.total != int64(len(content)) {
			t.Errorf("Progress total = %d, want %d", pc.total, len(content))
			break
		}
	}
}

func TestDownloadWithProgress_ContextCancellation(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second) // Slow response
		w.Write([]byte("data"))
	}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := downloadWithProgress(ctx, ts.URL, nil)
	if err == nil {
		t.Error("Expected error on context cancellation")
	}
}

// ── EnsureEngine Tests ──────────────────────────────────────────────────────────

func TestEnsureEngine_ReturnsNil_WhenFileExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sing-box.exe")

	// Create existing file >= MinValidBinarySize (1MB) so NeedsDownload returns false
	if err := os.WriteFile(path, make([]byte, 1024*1024), 0755); err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	err := EnsureEngine(context.Background(), path, nil)
	if err != nil {
		t.Errorf("EnsureEngine should return nil for existing file, got: %v", err)
	}
}

func TestEnsureEngine_SendsProgress(t *testing.T) {
	oldVerify := verifySingBoxBinaryFn
	verifySingBoxBinaryFn = func(_ context.Context, _ string) error { return nil }
	defer func() { verifySingBoxBinaryFn = oldVerify }()

	dir := t.TempDir()
	path := filepath.Join(dir, "sing-box.exe")

	// Create mock server
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "releases") {
			release := githubRelease{
				TagName: "v1.13.7",
				Assets: []githubAsset{
					{Name: "sing-box-1.13.7-windows-amd64.zip", DownloadURL: ts.URL + "/download"},
				},
			}
			json.NewEncoder(w).Encode(release)
			return
		}

		// Create zip with sing-box.exe
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		fw, _ := zw.Create("sing-box.exe")
		fw.Write([]byte("fake exe"))
		zw.Close()

		w.Header().Set("Content-Length", fmt.Sprintf("%d", buf.Len()))
		w.Write(buf.Bytes())
	}))
	defer ts.Close()

	oldAPI := githubAPI
	githubAPI = ts.URL + "/releases/latest"
	defer func() { githubAPI = oldAPI }()

	progressChan := make(chan Progress, 20)
	go func() {
		for p := range progressChan {
			t.Logf("Progress: %s - %s (%d%%)", p.Stage, p.Message, p.Percent)
		}
	}()

	err := EnsureEngine(context.Background(), path, progressChan)
	close(progressChan)

	if err != nil {
		t.Errorf("EnsureEngine failed: %v", err)
	}
}

func TestEnsureEngine_ExposeBusyStateUntilVerificationFinishes(t *testing.T) {
	enteredVerify := make(chan struct{})
	releaseVerify := make(chan struct{})
	oldVerify := verifySingBoxBinaryFn
	verifySingBoxBinaryFn = func(_ context.Context, _ string) error {
		close(enteredVerify)
		<-releaseVerify
		return nil
	}
	defer func() { verifySingBoxBinaryFn = oldVerify }()

	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "releases") {
			release := githubRelease{
				TagName: "v1.13.7",
				Assets: []githubAsset{
					{Name: "sing-box-1.13.7-windows-amd64.zip", DownloadURL: ts.URL + "/download"},
				},
			}
			_ = json.NewEncoder(w).Encode(release)
			return
		}

		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		fw, _ := zw.Create("sing-box.exe")
		_, _ = fw.Write([]byte("fake exe"))
		_ = zw.Close()
		_, _ = w.Write(buf.Bytes())
	}))
	defer ts.Close()

	oldAPI := githubAPI
	githubAPI = ts.URL + "/releases/latest"
	defer func() { githubAPI = oldAPI }()

	execPath := filepath.Join(t.TempDir(), "sing-box.exe")
	errCh := make(chan error, 1)
	go func() {
		errCh <- EnsureEngine(context.Background(), execPath, nil)
	}()

	select {
	case <-enteredVerify:
	case <-time.After(5 * time.Second):
		t.Fatal("EnsureEngine did not reach verification")
	}
	if !EnsureInProgress() {
		t.Fatal("EnsureInProgress() = false while verification is blocked")
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := WaitForEnsure(waitCtx); err == nil {
		t.Fatal("WaitForEnsure should block until verification finishes")
	}

	close(releaseVerify)
	if err := <-errCh; err != nil {
		t.Fatalf("EnsureEngine: %v", err)
	}
	if err := WaitForEnsure(context.Background()); err != nil {
		t.Fatalf("WaitForEnsure after completion: %v", err)
	}
	if EnsureInProgress() {
		t.Fatal("EnsureInProgress() = true after EnsureEngine completion")
	}
}

// ── progressReader Tests ────────────────────────────────────────────────────────

func TestProgressReader_ReportsProgress(t *testing.T) {
	content := make([]byte, 300*1024) // 300KB
	var callbacks int

	pr := &progressReader{
		r:          bytes.NewReader(content),
		total:      int64(len(content)),
		onProgress: func(d, total int64) { callbacks++ },
	}

	buf := make([]byte, 1024)
	for {
		_, err := pr.Read(buf)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Read failed: %v", err)
		}
	}

	// Should have at least one callback (256KB threshold)
	if callbacks < 1 {
		t.Error("Expected at least one progress callback")
	}
}

func TestProgressReader_HandlesNilCallback(t *testing.T) {
	content := []byte("test content")
	pr := &progressReader{
		r:     bytes.NewReader(content),
		total: int64(len(content)),
	}

	buf := make([]byte, 1024)
	n, err := pr.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatalf("Read failed: %v", err)
	}
	if n == 0 {
		t.Error("Expected to read some bytes")
	}
}

// ── fetchChecksumFile Tests ─────────────────────────────────────────────────────

func TestFetchChecksumFile_ReturnsChecksum(t *testing.T) {
	expectedSum := strings.Repeat("a", 64)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(expectedSum + "  filename.zip"))
	}))
	defer ts.Close()

	sum, err := fetchChecksumFile(context.Background(), ts.URL, &http.Client{Transport: noProxyTransport})
	if err != nil {
		t.Fatalf("fetchChecksumFile failed: %v", err)
	}

	if sum != expectedSum {
		t.Errorf("Checksum = %q, want %q", sum, expectedSum)
	}
}

func TestFetchChecksumFile_ReturnsError_OnHTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	_, err := fetchChecksumFile(context.Background(), ts.URL, &http.Client{Transport: noProxyTransport})
	if err == nil {
		t.Error("Expected error on HTTP 500")
	}
}

func TestFetchChecksumFile_ReturnsError_OnInvalidFormat(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("too short"))
	}))
	defer ts.Close()

	_, err := fetchChecksumFile(context.Background(), ts.URL, &http.Client{Transport: noProxyTransport})
	if err == nil {
		t.Error("Expected error on invalid format")
	}
}

// ── Edge Cases ──────────────────────────────────────────────────────────────────

func TestEnsureEngine_CreatesDirectory(t *testing.T) {
	oldVerify := verifySingBoxBinaryFn
	verifySingBoxBinaryFn = func(_ context.Context, _ string) error { return nil }
	defer func() { verifySingBoxBinaryFn = oldVerify }()

	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "another", "sing-box.exe")

	// Directory doesn't exist yet
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("Expected parent directory to not exist")
	}

	// Create mock server
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "releases") {
			release := githubRelease{
				TagName: "v1.13.7",
				Assets: []githubAsset{
					{Name: "sing-box-1.13.7-windows-amd64.zip", DownloadURL: ts.URL + "/download"},
				},
			}
			json.NewEncoder(w).Encode(release)
			return
		}

		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		fw, _ := zw.Create("sing-box.exe")
		fw.Write([]byte("fake exe"))
		zw.Close()
		w.Write(buf.Bytes())
	}))
	defer ts.Close()

	oldAPI := githubAPI
	githubAPI = ts.URL + "/releases/latest"
	defer func() { githubAPI = oldAPI }()

	err := EnsureEngine(context.Background(), path, nil)
	if err != nil {
		t.Fatalf("EnsureEngine failed: %v", err)
	}

	// Verify directory was created
	if _, err := os.Stat(filepath.Dir(path)); os.IsNotExist(err) {
		t.Error("Expected parent directory to be created")
	}
}

// ── A-2: withRetry Tests ────────────────────────────────────────────────────────

func TestWithRetry_SuccessOnFirstAttempt(t *testing.T) {
	calls := 0
	err := withRetry(context.Background(), 3, func() error {
		calls++
		return nil
	})
	if err != nil {
		t.Errorf("withRetry unexpected error: %v", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

func TestWithRetry_SuccessOnThirdAttempt(t *testing.T) {
	old := retryBaseDelay
	retryBaseDelay = time.Millisecond
	defer func() { retryBaseDelay = old }()

	calls := 0
	err := withRetry(context.Background(), 3, func() error {
		calls++
		if calls < 3 {
			return fmt.Errorf("временная ошибка попытки %d", calls)
		}
		return nil
	})
	if err != nil {
		t.Errorf("withRetry должен успешно завершиться на 3-й попытке, got: %v", err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}

func TestWithRetry_AllAttemptsFail(t *testing.T) {
	old := retryBaseDelay
	retryBaseDelay = time.Millisecond
	defer func() { retryBaseDelay = old }()

	calls := 0
	err := withRetry(context.Background(), 3, func() error {
		calls++
		return fmt.Errorf("постоянная ошибка")
	})
	if err == nil {
		t.Error("withRetry должен вернуть ошибку когда все попытки провалились")
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}

func TestWithRetry_CanceledContextStopsRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // отменяем сразу

	calls := 0
	err := withRetry(ctx, 3, func() error {
		calls++
		return fmt.Errorf("ошибка")
	})
	if err == nil {
		t.Error("withRetry должен вернуть ошибку контекста")
	}
	// Первая попытка может выполниться до проверки ctx.Err()
	if calls > 1 {
		t.Errorf("при отменённом контексте должна быть ≤1 попытки, got %d", calls)
	}
}

func TestWithRetry_ContextCanceledDuringBackoff(t *testing.T) {
	old := retryBaseDelay
	retryBaseDelay = 500 * time.Millisecond
	defer func() { retryBaseDelay = old }()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	calls := 0
	err := withRetry(ctx, 5, func() error {
		calls++
		return fmt.Errorf("ошибка")
	})
	if err == nil {
		t.Error("withRetry должен завершиться с ошибкой при таймауте")
	}
	// Должна быть только 1 попытка — backoff 500ms прерывается таймаутом 100ms
	if calls > 2 {
		t.Errorf("calls = %d, want ≤2 (backoff должен прерваться)", calls)
	}
}

// A-2: EnsureEngine с retry — первые 2 запроса 503, третий 200 → успех
func TestEnsureEngine_Retries_OnServerErrors(t *testing.T) {
	old := retryBaseDelay
	retryBaseDelay = time.Millisecond
	defer func() { retryBaseDelay = old }()

	oldVerify := verifySingBoxBinaryFn
	verifySingBoxBinaryFn = func(_ context.Context, _ string) error { return nil }
	defer func() { verifySingBoxBinaryFn = oldVerify }()

	dir := t.TempDir()
	path := filepath.Join(dir, "sing-box.exe")

	var callCount int
	var mu sync.Mutex

	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		callCount++
		n := callCount
		mu.Unlock()

		// Первые два запроса к metadata → 503
		if strings.Contains(r.URL.Path, "releases") || r.URL.Path == "/" {
			if n <= 2 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			// Третий → успех
			release := githubRelease{
				TagName: "v1.13.7",
				Assets: []githubAsset{
					{Name: "sing-box-1.13.7-windows-amd64.zip", DownloadURL: ts.URL + "/dl"},
				},
			}
			json.NewEncoder(w).Encode(release)
			return
		}
		// Download endpoint
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		fw, _ := zw.Create("sing-box.exe")
		fw.Write([]byte("fake exe v1.13.7"))
		zw.Close()
		w.Write(buf.Bytes())
	}))
	defer ts.Close()

	oldAPI := githubAPI
	githubAPI = ts.URL
	defer func() { githubAPI = oldAPI }()

	if err := EnsureEngine(context.Background(), path, nil); err != nil {
		t.Fatalf("EnsureEngine должен успешно завершиться на 3-й попытке, got: %v", err)
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("sing-box.exe должен быть создан")
	}
}

// ── A-3: InstalledVersion / version file Tests ──────────────────────────────────

func TestInstalledVersion_ReturnsEmpty_WhenNoFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sing-box.exe")
	if v := InstalledVersion(path); v != "" {
		t.Errorf("InstalledVersion = %q, want empty (файл версии не существует)", v)
	}
}

func TestInstalledVersion_ReturnsVersion_WhenFileExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sing-box.exe")
	versionFile := path + ".version"

	if err := os.WriteFile(versionFile, []byte("v1.9.0\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if v := InstalledVersion(path); v != "v1.9.0" {
		t.Errorf("InstalledVersion = %q, want v1.9.0", v)
	}
}

func TestEnsureEngine_WritesVersionFile(t *testing.T) {
	oldVerify := verifySingBoxBinaryFn
	verifySingBoxBinaryFn = func(_ context.Context, _ string) error { return nil }
	defer func() { verifySingBoxBinaryFn = oldVerify }()

	dir := t.TempDir()
	path := filepath.Join(dir, "sing-box.exe")

	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "releases") || r.URL.Path == "/" {
			release := githubRelease{
				TagName: "v1.13.7",
				Assets: []githubAsset{
					{Name: "sing-box-1.13.7-windows-amd64.zip", DownloadURL: ts.URL + "/dl"},
				},
			}
			json.NewEncoder(w).Encode(release)
			return
		}
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		fw, _ := zw.Create("sing-box.exe")
		fw.Write([]byte("fake exe"))
		zw.Close()
		w.Write(buf.Bytes())
	}))
	defer ts.Close()

	oldAPI := githubAPI
	githubAPI = ts.URL
	defer func() { githubAPI = oldAPI }()

	if err := EnsureEngine(context.Background(), path, nil); err != nil {
		t.Fatalf("EnsureEngine failed: %v", err)
	}

	if v := InstalledVersion(path); v != "v1.13.7" {
		t.Errorf("InstalledVersion после EnsureEngine = %q, want v1.13.7", v)
	}
}

// TestEnsureEngine_BinaryVerificationFails проверяет что при провале верификации бинарника
// EnsureEngine возвращает ошибку и удаляет повреждённый файл (чтобы следующий запуск
// перескачал бинарник заново через NeedsDownload).
func TestEnsureEngine_BinaryVerificationFails(t *testing.T) {
	old := retryBaseDelay
	retryBaseDelay = time.Millisecond
	defer func() { retryBaseDelay = old }()

	oldVerify := verifySingBoxBinaryFn
	verifySingBoxBinaryFn = func(_ context.Context, _ string) error {
		return fmt.Errorf("exit status 0xc0000005")
	}
	defer func() { verifySingBoxBinaryFn = oldVerify }()

	dir := t.TempDir()
	path := filepath.Join(dir, "sing-box.exe")

	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "releases") || r.URL.Path == "/" {
			release := githubRelease{
				TagName: "v1.13.5",
				Assets: []githubAsset{
					{Name: "sing-box-1.13.5-windows-amd64.zip", DownloadURL: ts.URL + "/dl"},
				},
			}
			json.NewEncoder(w).Encode(release)
			return
		}
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		fw, _ := zw.Create("sing-box.exe")
		fw.Write([]byte("fake exe"))
		zw.Close()
		w.Write(buf.Bytes())
	}))
	defer ts.Close()

	oldAPI := githubAPI
	githubAPI = ts.URL
	defer func() { githubAPI = oldAPI }()

	var lastProgress Progress
	progress := make(chan Progress, 50)

	// Дрейним прогресс в отдельной горутине — sync.WaitGroup гарантирует
	// что чтение lastProgress произойдёт только после завершения горутины.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for p := range progress {
			lastProgress = p
		}
	}()

	err := EnsureEngine(context.Background(), path, progress)
	close(progress)
	wg.Wait()

	if err == nil {
		t.Fatal("EnsureEngine должен вернуть ошибку при провале верификации бинарника")
	}

	// Файл должен быть удалён чтобы следующий запуск перескачал его
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Error("EnsureEngine должен удалить повреждённый файл, но он всё ещё существует")
	}

	// Последний прогресс должен быть стадии "error"
	if lastProgress.Stage != "error" {
		t.Errorf("последний stage = %q, want error", lastProgress.Stage)
	}
}

// ── Fuzz Tests ──────────────────────────────────────────────────────────────────

func FuzzFmtBytes(f *testing.F) {
	seeds := []int64{0, 1, 100, 1023, 1024, 1024 * 1024, 10 * 1024 * 1024, -1, -100}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, n int64) {
		result := fmtBytes(n)
		// Should always return something
		if result == "" {
			t.Error("fmtBytes returned empty string")
		}
	})
}

// ── БАГ 5: fetchLatestAssetFallback SHA256 ────────────────────────────────────

// TestFetchLatestAssetFallback_PopulatesChecksum проверяет что при наличии sha256sums.txt
// на сервере checksum заполняется в возвращённом asset'е.
func TestFetchLatestAssetFallback_PopulatesChecksum(t *testing.T) {
	ver := strings.TrimPrefix(pinnedVersion, "v")
	zipName := fmt.Sprintf("sing-box-%s-%s-%s.zip", ver, assetOS, assetArch)
	expectedSum := strings.Repeat("a", 64)

	// Имитируем sha256sums.txt сервера GitHub
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Возвращаем sha256sums.txt с нашей суммой для нужного файла
		fmt.Fprintf(w, "%s  %s\n", expectedSum, zipName)
	}))
	defer ts.Close()

	// Подменяем pinnedVersion URL на тестовый сервер через monkey-patching суммы URL.
	// fetchLatestAssetFallback строит URL на основе pinnedVersion и github.com — мы не можем
	// его подменить напрямую, поэтому тестируем через fetchChecksumFromSums напрямую.
	client := ts.Client()
	sum, err := fetchChecksumFromSums(context.Background(), ts.URL+"/sha256sums.txt", zipName, client)
	if err != nil {
		t.Fatalf("fetchChecksumFromSums failed: %v", err)
	}
	if sum != expectedSum {
		t.Errorf("sum = %q, want %q", sum, expectedSum)
	}
}

// TestFetchLatestAssetFallback_NoChecksumOnError проверяет что при недоступности
// sha256sums.txt asset всё равно возвращается (без Checksum) — без ошибки.
func TestFetchLatestAssetFallback_NoChecksumOnError(t *testing.T) {
	// Сервер возвращает 404 для sha256sums.txt
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer ts.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	// fetchLatestAssetFallback не должна возвращать ошибку даже если sha256sums недоступен.
	// Тестируем косвенно через fetchChecksumFromSums — ожидаем ошибку HTTP 404.
	_, err := fetchChecksumFromSums(context.Background(), ts.URL+"/sha256sums.txt", "any.zip", client)
	if err == nil {
		t.Error("ожидалась ошибка при HTTP 404")
	}
	// При такой ошибке fetchLatestAssetFallback должна продолжать без Checksum (не возвращать ошибку).
	// Проверяем поведение всей функции: она должна вернуть asset с пустым Checksum.
	orig := githubAPI
	githubAPI = ts.URL + "/api"
	defer func() { githubAPI = orig }()

	asset, version, fallbackErr := fetchLatestAssetFallback(context.Background(), client)
	if fallbackErr != nil {
		t.Errorf("fetchLatestAssetFallback не должна возвращать ошибку: %v", fallbackErr)
	}
	if asset.Name == "" {
		t.Error("asset.Name должен быть заполнен")
	}
	if version == "" {
		t.Error("version должна быть заполнена")
	}
	// Checksum может быть пустым — это нормально при недоступном sha256sums.txt
	_ = asset.Checksum
}

func FuzzVerifyChecksum(f *testing.F) {
	data := []byte("test data")
	sum := sha256.Sum256(data)
	expected := hex.EncodeToString(sum[:])

	f.Add(data, expected)
	f.Add(data, "wrong checksum")
	f.Add([]byte{}, expected)
	f.Add([]byte{}, "")

	f.Fuzz(func(t *testing.T, data []byte, expected string) {
		// Function should not panic
		_ = verifyChecksum(data, expected)
	})
}
