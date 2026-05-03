package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const maxTelemetryBody = 100 << 10

type event struct {
	Type            string    `json:"type"`
	Timestamp       time.Time `json:"ts"`
	Protocol        string    `json:"protocol,omitempty"`
	Transport       string    `json:"transport,omitempty"`
	Code            string    `json:"code,omitempty"`
	Stage           string    `json:"stage,omitempty"`
	DurationSeconds int64     `json:"duration_seconds,omitempty"`
	BytesUp         int64     `json:"bytes_up,omitempty"`
	BytesDown       int64     `json:"bytes_down,omitempty"`
}

type payload struct {
	AnonymousID          string  `json:"anonymous_id"`
	ClientVersion        string  `json:"client_version"`
	OSVersion            string  `json:"os_version"`
	Locale               string  `json:"locale"`
	Events               []event `json:"events"`
	SessionUptimeSeconds int64   `json:"session_uptime_seconds"`
}

type server struct {
	db *sql.DB
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8090", "listen address")
	dbPath := flag.String("db", "telemetry.db", "sqlite database path")
	flag.Parse()
	db, err := openDB(*dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	s := &server{db: db}
	mux := http.NewServeMux()
	s.routes(mux)
	log.Printf("telemetry server listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if err := initSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func initSchema(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (
			anonymous_id TEXT PRIMARY KEY,
			client_version TEXT,
			os_version TEXT,
			locale TEXT,
			first_seen TEXT NOT NULL,
			last_seen TEXT NOT NULL,
			session_uptime_seconds INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			anonymous_id TEXT NOT NULL,
			type TEXT NOT NULL,
			ts TEXT NOT NULL,
			protocol TEXT,
			transport TEXT,
			code TEXT,
			stage TEXT,
			duration_seconds INTEGER,
			bytes_up INTEGER,
			bytes_down INTEGER,
			raw_json TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_events_anon ON events(anonymous_id)`,
		`CREATE INDEX IF NOT EXISTS idx_events_type_ts ON events(type, ts)`,
		`CREATE TABLE IF NOT EXISTS crash_reports (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			anonymous_id TEXT NOT NULL,
			received_at TEXT NOT NULL,
			report_json TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_crash_reports_anon ON crash_reports(anonymous_id)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *server) routes(mux *http.ServeMux) {
	mux.HandleFunc("/api/telemetry/v1", s.handleIngest)
	mux.HandleFunc("/api/telemetry/v1/crash", s.handleCrash)
	mux.HandleFunc("/api/telemetry/v1/delete", s.handleDelete)
	mux.HandleFunc("/api/telemetry/v1/export", s.handleExport)
	mux.HandleFunc("/", s.handleAdmin)
}

func (s *server) handleCrash(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		AnonymousID string          `json:"anonymous_id"`
		Report      json.RawMessage `json:"report"`
	}
	if err := decodeLimited(w, r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.AnonymousID) == "" || len(req.Report) == 0 {
		http.Error(w, "anonymous_id and report are required", http.StatusBadRequest)
		return
	}
	if _, err := s.db.ExecContext(r.Context(), `INSERT INTO crash_reports(anonymous_id, received_at, report_json) VALUES(?,?,?)`,
		req.AnonymousID, time.Now().UTC().Format(time.RFC3339), string(req.Report)); err != nil {
		http.Error(w, "store crash report", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *server) handleIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var p payload
	if err := decodeLimited(w, r, &p); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validatePayload(p); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.store(r.Context(), p); err != nil {
		http.Error(w, "store telemetry", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func decodeLimited(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxTelemetryBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra struct{}
	if err := dec.Decode(&extra); err != io.EOF {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}

func validatePayload(p payload) error {
	if strings.TrimSpace(p.AnonymousID) == "" {
		return fmt.Errorf("anonymous_id is required")
	}
	if len(p.Events) == 0 || len(p.Events) > 100 {
		return fmt.Errorf("events must contain 1..100 items")
	}
	for _, ev := range p.Events {
		if strings.TrimSpace(ev.Type) == "" {
			return fmt.Errorf("event type is required")
		}
	}
	return nil
}

func (s *server) store(ctx context.Context, p payload) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.ExecContext(ctx, `INSERT INTO users(anonymous_id, client_version, os_version, locale, first_seen, last_seen, session_uptime_seconds)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(anonymous_id) DO UPDATE SET client_version=excluded.client_version, os_version=excluded.os_version, locale=excluded.locale, last_seen=excluded.last_seen, session_uptime_seconds=excluded.session_uptime_seconds`,
		p.AnonymousID, p.ClientVersion, p.OSVersion, p.Locale, now, now, p.SessionUptimeSeconds); err != nil {
		return err
	}
	for _, ev := range p.Events {
		raw, _ := json.Marshal(ev)
		ts := ev.Timestamp.UTC().Format(time.RFC3339)
		if ev.Timestamp.IsZero() {
			ts = now
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO events(anonymous_id,type,ts,protocol,transport,code,stage,duration_seconds,bytes_up,bytes_down,raw_json)
			VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			p.AnonymousID, ev.Type, ts, ev.Protocol, ev.Transport, ev.Code, ev.Stage, ev.DurationSeconds, ev.BytesUp, ev.BytesDown, string(raw)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *server) handleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		AnonymousID string `json:"anonymous_id"`
	}
	if err := decodeLimited(w, r, &req); err != nil || strings.TrimSpace(req.AnonymousID) == "" {
		http.Error(w, "anonymous_id is required", http.StatusBadRequest)
		return
	}
	_, _ = s.db.ExecContext(r.Context(), `DELETE FROM events WHERE anonymous_id=?`, req.AnonymousID)
	_, _ = s.db.ExecContext(r.Context(), `DELETE FROM crash_reports WHERE anonymous_id=?`, req.AnonymousID)
	_, _ = s.db.ExecContext(r.Context(), `DELETE FROM users WHERE anonymous_id=?`, req.AnonymousID)
	_ = json.NewEncoder(w).Encode(map[string]bool{"deleted": true})
}

func (s *server) handleExport(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("anonymous_id"))
	if id == "" {
		http.Error(w, "anonymous_id is required", http.StatusBadRequest)
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT raw_json FROM events WHERE anonymous_id=? ORDER BY ts ASC LIMIT 10000`, id)
	if err != nil {
		http.Error(w, "query export", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	var events []json.RawMessage
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			http.Error(w, "scan export", http.StatusInternalServerError)
			return
		}
		events = append(events, json.RawMessage(raw))
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"anonymous_id": id, "events": events})
}

func (s *server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	var stats struct {
		Users  int
		Events int
		Types  []struct {
			Type  string
			Count int
		}
	}
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&stats.Users)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&stats.Events)
	rows, err := s.db.Query(`SELECT type, COUNT(*) FROM events GROUP BY type ORDER BY COUNT(*) DESC LIMIT 20`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var row struct {
				Type  string
				Count int
			}
			if rows.Scan(&row.Type, &row.Count) == nil {
				stats.Types = append(stats.Types, row)
			}
		}
	}
	_ = adminTpl.Execute(w, stats)
}

var adminTpl = template.Must(template.New("admin").Parse(`<!doctype html><meta charset="utf-8"><title>SafeSky telemetry</title>
<h1>SafeSky telemetry</h1>
<p>Users: {{.Users}} · Events: {{.Events}}</p>
<table border="1" cellpadding="6"><tr><th>Event type</th><th>Count</th></tr>{{range .Types}}<tr><td>{{.Type}}</td><td>{{.Count}}</td></tr>{{end}}</table>`))
