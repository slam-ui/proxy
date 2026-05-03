package telemetry

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBufferCapacityAndDrain(t *testing.T) {
	buf := NewBuffer(2)
	if flush := buf.Add(Event{Type: "connect_success"}); flush {
		t.Fatal("first event should not trigger flush")
	}
	if flush := buf.Add(Event{Type: "session_end"}); !flush {
		t.Fatal("second event should trigger flush")
	}
	if got := buf.Len(); got != 2 {
		t.Fatalf("Len=%d, want 2", got)
	}
	events := buf.Drain(1)
	if len(events) != 1 || events[0].Type != "connect_success" {
		t.Fatalf("drain first=%+v", events)
	}
	if got := buf.Len(); got != 1 {
		t.Fatalf("Len after drain=%d, want 1", got)
	}
}

func TestEnsureAnonymousIDOnlyWhenEnabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry_id")
	id, err := (Client{Enabled: false, AnonymousPath: path}).EnsureAnonymousID()
	if err != nil {
		t.Fatalf("EnsureAnonymousID opt-out: %v", err)
	}
	if id != "" {
		t.Fatalf("id=%q, want empty", id)
	}
	id, err = (Client{Enabled: true, AnonymousPath: path}).EnsureAnonymousID()
	if err != nil {
		t.Fatalf("EnsureAnonymousID opt-in: %v", err)
	}
	if len(id) != 36 {
		t.Fatalf("id=%q", id)
	}
	again, err := (Client{Enabled: true, AnonymousPath: path}).EnsureAnonymousID()
	if err != nil {
		t.Fatalf("EnsureAnonymousID reload: %v", err)
	}
	if again != id {
		t.Fatalf("id changed: %q -> %q", id, again)
	}
}

func TestFlushOptOutDoesNotSendRequest(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	defer srv.Close()
	err := (Client{Enabled: false, BaseURL: srv.URL, HTTPClient: srv.Client()}).Flush(t.Context(), Payload{
		AnonymousID: "id",
		Events:      []Event{{Type: "connect_success", Timestamp: time.Now()}},
	})
	if err != nil {
		t.Fatalf("Flush opt-out: %v", err)
	}
	if called {
		t.Fatal("opt-out telemetry sent a request")
	}
}

func TestFlushSendsPrivacySafePayload(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != DefaultEndpointPath {
			t.Fatalf("path=%s, want %s", r.URL.Path, DefaultEndpointPath)
		}
		data, _ := io.ReadAll(r.Body)
		body = string(data)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	payload := Payload{
		AnonymousID:          "00000000-0000-4000-8000-000000000000",
		ClientVersion:        "0.9.3",
		OSVersion:            "Windows 11",
		Locale:               "ru",
		SessionUptimeSeconds: 10,
		Events: []Event{{
			Type:      "connect_failed",
			Timestamp: time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC),
			Protocol:  "vless",
			Transport: "grpc",
			Code:      "TLS_HANDSHAKE_FAILED",
			Stage:     "handshake",
		}},
	}
	err := (Client{Enabled: true, BaseURL: srv.URL, HTTPClient: srv.Client()}).Flush(t.Context(), payload)
	if err != nil {
		t.Fatalf("Flush: %v", err)
	}
	for _, forbidden := range []string{"server_name", "hostname", "password", "subscription", "ip_address", "domain"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("payload contains forbidden field %q: %s", forbidden, body)
		}
	}
	var decoded Payload
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if decoded.Events[0].Protocol != "vless" || decoded.Events[0].Transport != "grpc" {
		t.Fatalf("decoded payload=%+v", decoded)
	}
}

func TestManagerFlushBuildsPayloadAndDrains(t *testing.T) {
	var decoded Payload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&decoded); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	m := NewManager(ManagerConfig{
		Client: Client{
			Enabled:       true,
			BaseURL:       srv.URL,
			AnonymousPath: filepath.Join(t.TempDir(), "telemetry_id"),
			HTTPClient:    srv.Client(),
		},
		ClientVersion: "1.2.3",
		OSVersion:     "Windows 11",
		Locale:        "en",
		Now:           func() time.Time { return now },
	})
	m.Record(Event{Type: "connect_success", Protocol: "vless", Transport: "grpc"})
	if err := m.Flush(t.Context()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if m.Len() != 0 {
		t.Fatalf("buffer len=%d, want 0", m.Len())
	}
	if decoded.ClientVersion != "1.2.3" || decoded.Locale != "en" {
		t.Fatalf("payload metadata=%+v", decoded)
	}
	if len(decoded.Events) != 1 || decoded.Events[0].Timestamp.IsZero() {
		t.Fatalf("payload events=%+v", decoded.Events)
	}
}

func TestManagerOptOutDoesNotBuffer(t *testing.T) {
	m := NewManager(ManagerConfig{Client: Client{Enabled: false}})
	m.Record(Event{Type: "connect_success"})
	if m.Len() != 0 {
		t.Fatalf("opt-out manager buffered events: %d", m.Len())
	}
}
