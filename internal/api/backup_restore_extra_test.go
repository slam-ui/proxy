package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"proxyclient/internal/logger"
)

func makeBackupZip(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func postBackupRestore(t *testing.T, srv *Server, zipData []byte, overwrite bool) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if overwrite {
		if err := mw.WriteField("overwrite", "true"); err != nil {
			t.Fatalf("WriteField overwrite: %v", err)
		}
	}
	part, err := mw.CreateFormFile("file", "backup.zip")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(zipData); err != nil {
		t.Fatalf("Write zip: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("multipart close: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/backup/restore", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	srv.handleBackupRestore(w, req)
	return w
}

func TestHandleBackupRestore_RejectsMissingMeta(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer os.Chdir(old)

	srv, cleanup := newProxySrv(t)
	defer cleanup()

	w := postBackupRestore(t, srv, makeBackupZip(t, map[string][]byte{
		"data/routing.json": []byte(`{"default_action":"direct","rules":[]}`),
	}), true)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400: %s", w.Code, w.Body.String())
	}
}

func TestHandleBackupRestore_ZipSlipEntrySkipped(t *testing.T) {
	parent := t.TempDir()
	workDir := filepath.Join(parent, "work")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatalf("MkdirAll work: %v", err)
	}
	old, _ := os.Getwd()
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer os.Chdir(old)

	srv, cleanup := newProxySrv(t)
	defer cleanup()

	meta, _ := json.Marshal(map[string]int{"schema_version": 1})
	zipData := makeBackupZip(t, map[string][]byte{
		"backup_meta.json": meta,
		"../escape.txt":    []byte("owned"),
	})

	w := postBackupRestore(t, srv, zipData, true)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(parent, "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("zip slip file was created outside workdir, stat err=%v", err)
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if skipped, _ := resp["skipped"].(float64); skipped < 1 {
		t.Fatalf("skipped = %v, want at least 1", resp["skipped"])
	}
}

func TestBackupRestoreRoute_AllowsFiveMegabyteUploads(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer os.Chdir(old)

	srv := NewServer(Config{
		XRayManager:  &stubXray{},
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
	}, context.Background())
	srv.SetupFeatureRoutes(context.Background())
	srv.FinalizeRoutes()

	meta, _ := json.Marshal(map[string]int{"schema_version": 1})
	largeProfile := make([]byte, 3<<20)
	var x uint32 = 1
	for i := range largeProfile {
		x = x*1664525 + 1013904223
		largeProfile[i] = byte(x >> 24)
	}
	zipData := makeBackupZip(t, map[string][]byte{
		"backup_meta.json":  meta,
		"profiles/big.json": largeProfile,
	})

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("overwrite", "true"); err != nil {
		t.Fatalf("WriteField overwrite: %v", err)
	}
	part, err := mw.CreateFormFile("file", "backup.zip")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(zipData); err != nil {
		t.Fatalf("Write zip: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("multipart close: %v", err)
	}
	if body.Len() <= maxRequestBodyBytes || body.Len() >= maxBackupRequestBodyBytes {
		t.Fatalf("test body size %d must be between default and backup limits", body.Len())
	}

	req := httptest.NewRequest(http.MethodPost, "/api/backup/restore", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("POST /api/backup/restore = %d, want 200: %s", w.Code, w.Body.String())
	}
}
