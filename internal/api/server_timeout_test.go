package api

import (
	"context"
	"testing"
	"time"

	"proxyclient/internal/logger"
)

func TestServerHasReadTimeout(t *testing.T) {
	s := NewServer(Config{ListenAddress: "127.0.0.1:0", Logger: &logger.NoOpLogger{}}, context.Background())
	httpServer := s.newHTTPServer()

	if httpServer.ReadHeaderTimeout != 5*time.Second {
		t.Fatalf("ReadHeaderTimeout=%v, want 5s", httpServer.ReadHeaderTimeout)
	}
	if httpServer.ReadTimeout != 30*time.Second {
		t.Fatalf("ReadTimeout=%v, want 30s", httpServer.ReadTimeout)
	}
	if httpServer.ReadTimeout <= httpServer.ReadHeaderTimeout {
		t.Fatalf("ReadTimeout=%v must be greater than ReadHeaderTimeout=%v", httpServer.ReadTimeout, httpServer.ReadHeaderTimeout)
	}
}
