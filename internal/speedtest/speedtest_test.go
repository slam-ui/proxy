package speedtest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func withSpeedtestURL(t *testing.T, url string) {
	t.Helper()
	oldURL := speedtestDownloadURL
	speedtestDownloadURL = url
	t.Cleanup(func() {
		speedtestDownloadURL = oldURL
	})
}

func TestRunRejectsHTTPErrorStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer ts.Close()
	withSpeedtestURL(t, ts.URL)

	result := Run(context.Background(), "")
	if result.Error == "" {
		t.Fatalf("Run returned no error for status 500: %+v", result)
	}
}

func TestRunRejectsOversizedResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", int(speedtestDownloadBytes)+1)))
	}))
	defer ts.Close()
	withSpeedtestURL(t, ts.URL)

	result := Run(context.Background(), "")
	if result.Error == "" {
		t.Fatalf("Run returned no error for oversized response: %+v", result)
	}
}
