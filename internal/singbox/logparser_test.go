package singbox

import (
	"testing"

	"proxyclient/internal/errcodes"
)

func TestParseLogTailPatterns(t *testing.T) {
	tests := []struct {
		name string
		log  string
		code errcodes.Code
	}{
		{"reality", "INFO ok\nERROR REALITY: processed invalid connection", errcodes.RealityHandshakeFail},
		{"tcp timeout", "failed to start outbound[proxy-out]: dial tcp 1.2.3.4:443: i/o timeout", errcodes.TCPConnectFailed},
		{"tls", "remote error: tls: handshake failure", errcodes.TLSHandshakeFailed},
		{"unknown field", `FATAL decode config: unknown field "flowx"`, errcodes.KeyParseError},
		{"wintun", "inbound/tun: failed to start: open wintun: error", errcodes.TUNAdapterFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseLogTail(tt.log)
			if got == nil || got.Code != tt.code {
				t.Fatalf("ParseLogTail() = %+v, want %s", got, tt.code)
			}
		})
	}
}
