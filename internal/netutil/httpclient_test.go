package netutil

import (
	"net/http"
	"testing"
	"time"
)

func TestSharedHTTPClientReusesTransport(t *testing.T) {
	first := SharedHTTPClient(3 * time.Second)
	second := SharedHTTPClient(7 * time.Second)
	if first == second {
		t.Fatal("SharedHTTPClient returned the same client instance")
	}
	if first.Transport == nil {
		t.Fatal("first client transport is nil")
	}
	if first.Transport != second.Transport {
		t.Fatal("clients do not share transport")
	}
	if first.Transport != SharedHTTPTransport() {
		t.Fatal("client transport does not match shared transport")
	}
	if first.Timeout != 3*time.Second {
		t.Fatalf("first timeout = %s, want 3s", first.Timeout)
	}
	if second.Timeout != 7*time.Second {
		t.Fatalf("second timeout = %s, want 7s", second.Timeout)
	}
}

func TestSharedHTTPTransportTuning(t *testing.T) {
	transport, ok := SharedHTTPTransport().(*http.Transport)
	if !ok {
		t.Fatalf("shared transport type = %T, want *http.Transport", SharedHTTPTransport())
	}
	if transport.MaxIdleConns < 10 {
		t.Fatalf("MaxIdleConns = %d, want at least 10", transport.MaxIdleConns)
	}
	if transport.IdleConnTimeout != 90*time.Second {
		t.Fatalf("IdleConnTimeout = %s, want 90s", transport.IdleConnTimeout)
	}
}
