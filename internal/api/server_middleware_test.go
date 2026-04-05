//go:build windows
// +build windows

package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"proxyclient/internal/logger"

	"github.com/gorilla/mux"
)

// ── CORS Middleware ───────────────────────────────────────────────────────────

func TestCORSMiddleware_AllowsLocalhost(t *testing.T) {
	for _, origin := range []string{"http://localhost:8080", "http://127.0.0.1:8080"} {
		t.Run(origin, func(t *testing.T) {
			s := newMiddlewareServer(t)
			req := httptest.NewRequest("GET", "/test", nil)
			req.Header.Set("Origin", origin)
			rec := httptest.NewRecorder()
			s.router.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
			}
			if rec.Header().Get("Access-Control-Allow-Origin") != origin {
				t.Errorf("Allow-Origin = %q, want %q", rec.Header().Get("Access-Control-Allow-Origin"), origin)
			}
		})
	}
}

func TestCORSMiddleware_AllowsAppScheme(t *testing.T) {
	s := newMiddlewareServer(t)
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Origin", "app://")
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestCORSMiddleware_BlocksForeignOrigin(t *testing.T) {
	s := newMiddlewareServer(t)
	req := httptest.NewRequest("OPTIONS", "/test", nil)
	req.Header.Set("Origin", "https://evil.com")
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("Preflight Status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestCORSMiddleware_AllowsNoOrigin(t *testing.T) {
	s := newMiddlewareServer(t)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest("GET", "/test", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestCORSMiddleware_SetsAllowMethods(t *testing.T) {
	s := newMiddlewareServer(t)
	req := httptest.NewRequest("OPTIONS", "/test", nil)
	req.Header.Set("Origin", "http://localhost:8080")
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	methods := rec.Header().Get("Access-Control-Allow-Methods")
	if methods == "" {
		t.Error("Allow-Methods header is empty")
	}
	for _, m := range []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"} {
		if !strings.Contains(methods, m) {
			t.Errorf("Allow-Methods = %q, should contain %q", methods, m)
		}
	}
}

func TestCORSMiddleware_Table(t *testing.T) {
	tests := []struct {
		name           string
		origin         string
		method         string
		expectedStatus int
		shouldHaveCORS bool
	}{
		{"localhost", "http://localhost:8080", "GET", http.StatusOK, true},
		{"127.0.0.1", "http://127.0.0.1:8080", "GET", http.StatusOK, true},
		{"app scheme", "app://", "GET", http.StatusOK, true},
		{"no origin", "", "GET", http.StatusOK, false},
		{"evil.com OPTIONS", "https://evil.com", "OPTIONS", http.StatusForbidden, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newMiddlewareServer(t)
			req := httptest.NewRequest(tc.method, "/test", nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			rec := httptest.NewRecorder()
			s.router.ServeHTTP(rec, req)

			if rec.Code != tc.expectedStatus {
				t.Errorf("Status = %d, want %d", rec.Code, tc.expectedStatus)
			}
			hasCORS := rec.Header().Get("Access-Control-Allow-Origin") != ""
			if hasCORS != tc.shouldHaveCORS {
				t.Errorf("Has CORS header = %v, want %v", hasCORS, tc.shouldHaveCORS)
			}
		})
	}
}

// ── Recovery Middleware ────────────────────────────────────────────────────────

func TestRecoveryMiddleware_RecoversFromPanic(t *testing.T) {
	log := logger.New(logger.LevelInfo)
	s := &Server{logger: log, router: mux.NewRouter()}
	s.router.Use(s.recoveryMiddleware)
	s.router.HandleFunc("/panic", func(w http.ResponseWriter, r *http.Request) {
		panic("test panic")
	})

	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest("GET", "/panic", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	var resp ErrorResponse
	if err := jsonDecode(rec, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(resp.Error, "внутренняя ошибка") {
		t.Errorf("Error = %q, should contain 'внутренняя ошибка'", resp.Error)
	}
}

func TestRecoveryMiddleware_PassesNormalRequest(t *testing.T) {
	log := logger.New(logger.LevelInfo)
	s := &Server{logger: log, router: mux.NewRouter()}
	s.router.Use(s.recoveryMiddleware)
	s.router.HandleFunc("/normal", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest("GET", "/normal", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}
}

// ── Logging Middleware ─────────────────────────────────────────────────────────

func TestLoggingMiddleware_SkipsSilentPaths(t *testing.T) {
	log := logger.New(logger.LevelInfo)
	s := &Server{
		logger: log,
		router: mux.NewRouter(),
		config: Config{SilentPaths: []string{"/api/silent"}},
	}
	s.router.Use(s.loggingMiddleware)
	called := false
	s.router.HandleFunc("/api/silent", func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	s.router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/silent", nil))
	if !called {
		t.Error("Handler was not called")
	}
}

func TestLoggingMiddleware_LogsNonSilentPaths(t *testing.T) {
	log := logger.New(logger.LevelInfo)
	s := &Server{logger: log, router: mux.NewRouter()}
	s.router.Use(s.loggingMiddleware)
	s.router.HandleFunc("/api/noisy", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest("GET", "/api/noisy", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestLoggingMiddleware_SkipsStaticFiles(t *testing.T) {
	log := logger.New(logger.LevelInfo)
	s := &Server{logger: log, router: mux.NewRouter()}
	s.router.Use(s.loggingMiddleware)

	for _, path := range []string{"/app.js", "/style.css", "/favicon.ico", "/index.html"} {
		t.Run(path, func(t *testing.T) {
			s.router.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			rec := httptest.NewRecorder()
			s.router.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
			if rec.Code != http.StatusOK {
				t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
			}
		})
	}
}

// ── Fuzz ──────────────────────────────────────────────────────────────────────

func FuzzCORSMiddleware(f *testing.F) {
	for _, seed := range []string{"http://localhost:8080", "https://evil.com", "", "app://", "http://127.0.0.1:8080"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, origin string) {
		s := &Server{logger: logger.New(logger.LevelInfo), router: mux.NewRouter()}
		s.router.Use(s.corsMiddleware)
		s.router.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("Origin", origin)
		s.router.ServeHTTP(httptest.NewRecorder(), req)
	})
}

// ── Benchmarks ────────────────────────────────────────────────────────────────

func BenchmarkCORSMiddleware(b *testing.B) {
	s := &Server{logger: logger.New(logger.LevelInfo), router: mux.NewRouter()}
	s.router.Use(s.corsMiddleware)
	s.router.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Origin", "http://localhost:8080")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.router.ServeHTTP(httptest.NewRecorder(), req)
	}
}

func BenchmarkLoggingMiddleware(b *testing.B) {
	s := &Server{logger: logger.New(logger.LevelInfo), router: mux.NewRouter()}
	s.router.Use(s.loggingMiddleware)
	s.router.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	req := httptest.NewRequest("GET", "/test", nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.router.ServeHTTP(httptest.NewRecorder(), req)
	}
}

// ── Helper ────────────────────────────────────────────────────────────────────

// newMiddlewareServer создаёт минимальный Server с CORS+/test маршрутом.
func newMiddlewareServer(t *testing.T) *Server {
	t.Helper()
	s := &Server{logger: logger.New(logger.LevelInfo), router: mux.NewRouter()}
	s.router.Use(s.corsMiddleware)
	s.router.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	return s
}
