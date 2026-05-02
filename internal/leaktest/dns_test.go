package leaktest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestRunDNSLeakTestReportsProtected(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"resolvers":["1.1.1.1"]}`))
	}))
	defer srv.Close()
	report, err := RunDNSLeakTest(t.Context(), DNSConfig{
		Domain:            "dnsleak.example.test",
		ReportURL:         srv.URL + "/api/dnsleak/check",
		ExpectedResolvers: []string{"1.1.1.1"},
		HTTPClient:        srv.Client(),
		LookupHost:        func(context.Context, string) ([]string, error) { return nil, nil },
		Sleep:             func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatalf("RunDNSLeakTest: %v", err)
	}
	if report.Leaked || report.Status != "protected" {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestRunDNSLeakTestReportsLeak(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"resolvers":["8.8.8.8"]}`))
	}))
	defer srv.Close()
	report, err := RunDNSLeakTest(t.Context(), DNSConfig{
		Domain:            "dnsleak.example.test",
		ReportURL:         srv.URL + "/api/dnsleak/check",
		ExpectedResolvers: []string{"1.1.1.1"},
		HTTPClient:        srv.Client(),
		LookupHost:        func(context.Context, string) ([]string, error) { return nil, nil },
		Sleep:             func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatalf("RunDNSLeakTest: %v", err)
	}
	if !report.Leaked || report.Status != "leak" {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestRunDNSLeakTestRejectsHTTPReportURL(t *testing.T) {
	_, err := RunDNSLeakTest(t.Context(), DNSConfig{
		Domain:    "dnsleak.example.test",
		ReportURL: "http://example.test/api/dnsleak/check",
	})
	if err == nil {
		t.Fatal("RunDNSLeakTest accepted http report URL")
	}
}

func TestRunIPv6LeakTest(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("2001:db8::1"))
	}))
	defer srv.Close()
	client := srv.Client()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	client.Transport = rewriteTransport{base: client.Transport, host: u.Host}
	report, err := RunIPv6LeakTest(t.Context(), client)
	if err != nil {
		t.Fatalf("RunIPv6LeakTest: %v", err)
	}
	if !report.Available || !report.Leaked {
		t.Fatalf("unexpected report: %+v", report)
	}
}

type rewriteTransport struct {
	base http.RoundTripper
	host string
}

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = "https"
	req.URL.Host = rt.host
	return rt.base.RoundTrip(req)
}
