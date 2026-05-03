package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func newTestServer(t *testing.T) (*server, *http.ServeMux) {
	t.Helper()
	db, err := openDB(filepath.Join(t.TempDir(), "telemetry.db"))
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := &server{db: db}
	mux := http.NewServeMux()
	s.routes(mux)
	return s, mux
}

func TestIngestStoresAndExportsEvents(t *testing.T) {
	_, mux := newTestServer(t)
	body := []byte(`{"anonymous_id":"anon-1","client_version":"1.0.0","os_version":"Windows 11","locale":"en","events":[{"type":"connect_success","ts":"2026-05-03T12:00:00Z","protocol":"vless","transport":"grpc"}],"session_uptime_seconds":12}`)
	req := httptest.NewRequest(http.MethodPost, "/api/telemetry/v1", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("ingest status=%d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/telemetry/v1/export?anonymous_id=anon-1", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("export status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Events []json.RawMessage `json:"events"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	if len(resp.Events) != 1 {
		t.Fatalf("events=%d, want 1", len(resp.Events))
	}
}

func TestIngestRejectsOversizedBatch(t *testing.T) {
	_, mux := newTestServer(t)
	var events []map[string]string
	for i := 0; i < 101; i++ {
		events = append(events, map[string]string{"type": "connect_success"})
	}
	body, _ := json.Marshal(map[string]any{"anonymous_id": "anon-1", "events": events})
	req := httptest.NewRequest(http.MethodPost, "/api/telemetry/v1", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", w.Code)
	}
}

func TestDeleteRemovesUserEvents(t *testing.T) {
	_, mux := newTestServer(t)
	body := []byte(`{"anonymous_id":"anon-1","events":[{"type":"connect_success"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/telemetry/v1", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("ingest status=%d", w.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/telemetry/v1/delete", bytes.NewReader([]byte(`{"anonymous_id":"anon-1"}`)))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("delete status=%d", w.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/telemetry/v1/export?anonymous_id=anon-1", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if !bytes.Contains(w.Body.Bytes(), []byte(`"events":[]`)) && !bytes.Contains(w.Body.Bytes(), []byte(`"events":null`)) {
		t.Fatalf("export after delete=%s", w.Body.String())
	}
}
