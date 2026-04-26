package xray

import (
	"bytes"
	"io"
	"regexp"
	"sync"
	"time"
)

// healthTrackingWriter wraps an io.Writer and tracks connection errors for the HealthChecker.
// БАГ #3: парсит ERROR логи из sing-box и записывает их в HealthChecker.
type healthTrackingWriter struct {
	mu         sync.Mutex
	dst        io.Writer
	checker    *HealthChecker
	manager    *manager
	buf        []byte
	errorRe    *regexp.Regexp
	outboundRe *regexp.Regexp
}

// NewHealthTrackingWriter creates a writer that tracks connection errors.
func NewHealthTrackingWriter(dst io.Writer, checker *HealthChecker, mgr ...*manager) *healthTrackingWriter {
	var m *manager
	if len(mgr) > 0 {
		m = mgr[0]
	}
	return &healthTrackingWriter{
		dst:        dst,
		checker:    checker,
		manager:    m,
		buf:        make([]byte, 0, 4096),
		errorRe:    regexp.MustCompile(`ERROR.*connection.*(?:wsarecv|i/o timeout|dial tcp|bind:|timeout)`),
		outboundRe: regexp.MustCompile(`using outbound/([^ \[\]]+)`),
	}
}

// maxLineBuf ограничивает размер внутреннего буфера.
// Защита от OOM при аварийном дампе или бинарном выводе sing-box без '\n'.
const maxLineBuf = 64 * 1024 // 64 KB

// Write parses sing-box output for connection errors and updates HealthChecker.
func (h *healthTrackingWriter) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.buf = append(h.buf, p...)

	// BUG FIX: защита от неограниченного роста буфера при отсутствии '\n'.
	// При аварийном дампе или бинарном выводе sing-box buf мог расти до OOM.
	if len(h.buf) > maxLineBuf {
		if idx := bytes.LastIndexByte(h.buf[:len(h.buf)-1], '\n'); idx >= 0 {
			h.buf = h.buf[idx+1:]
		} else {
			h.buf = h.buf[len(h.buf)-maxLineBuf:]
		}
	}

	// Process each complete line
	for {
		idx := bytes.IndexByte(h.buf, '\n')
		if idx < 0 {
			break
		}

		line := h.buf[:idx]
		h.buf = h.buf[idx+1:]

		// Parse error lines
		if h.errorRe.Match(line) {
			lineStr := string(line)

			// Extract outbound name
			outbound := "unknown"
			matches := h.outboundRe.FindStringSubmatch(lineStr)
			if len(matches) > 1 {
				outbound = matches[1]
			}

			// Determine error type
			errorType := "connection_error"
			if bytes.Contains(line, []byte("wsarecv")) {
				errorType = "wsarecv"
			} else if bytes.Contains(line, []byte("i/o timeout")) || bytes.Contains(line, []byte("dial tcp")) {
				errorType = "timeout"
			} else if bytes.Contains(line, []byte("bind:")) {
				errorType = "bind_error"
			}

			if triggered := h.checker.RecordError(time.Now(), errorType, outbound, lineStr); triggered {
				if h.manager != nil {
					h.manager.healthAlertMu.Lock()
					fn := h.manager.healthAlertFn
					h.manager.healthAlertMu.Unlock()
					if fn != nil {
						go fn()
					}
				}
			}
		}
	}

	// Write to destination
	if h.dst != nil {
		return h.dst.Write(p)
	}
	return len(p), nil
}
