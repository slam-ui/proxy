package subscription

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func supported(uri string) bool {
	return strings.HasPrefix(uri, "vless://") ||
		strings.HasPrefix(uri, "trojan://") ||
		strings.HasPrefix(uri, "ss://")
}

func TestParseBodyPlainText(t *testing.T) {
	got := ParseBody([]byte("vless://id@example.com:443?encryption=none#one\nbad\nss://YWVzOnBhc3M@example.net:8388#two"), supported)
	if len(got.Servers) != 2 {
		t.Fatalf("servers=%d, want 2, warnings=%v", len(got.Servers), got.Warnings)
	}
	if got.Servers[0].URI == "" || len(got.Warnings) != 1 {
		t.Fatalf("unexpected result: %+v", got)
	}
}

func TestParseBodyBase64(t *testing.T) {
	body := base64.StdEncoding.EncodeToString([]byte("trojan://pass@example.com:443#trojan\n"))
	got := ParseBody([]byte(body), supported)
	if len(got.Servers) != 1 || !strings.HasPrefix(got.Servers[0].URI, "trojan://") {
		t.Fatalf("unexpected result: %+v", got)
	}
}

func TestParseBodySIP008(t *testing.T) {
	body := `{"version":1,"servers":[{"id":"one","remarks":"ams","server":"example.com","server_port":8388,"method":"aes-256-gcm","password":"secret"}]}`
	got := ParseBody([]byte(body), supported)
	if len(got.Servers) != 1 {
		t.Fatalf("servers=%d, warnings=%v", len(got.Servers), got.Warnings)
	}
	if got.Servers[0].Name != "ams" || !strings.HasPrefix(got.Servers[0].URI, "ss://") {
		t.Fatalf("unexpected server: %+v", got.Servers[0])
	}
}

func TestParseUserInfoHeader(t *testing.T) {
	got := ParseUserInfoHeader("upload=12345; download=67890; total=10737418240; expire=1746000000")
	if got.Upload != 12345 || got.Download != 67890 || got.Total != 10737418240 {
		t.Fatalf("unexpected quota: %+v", got)
	}
	want := time.Unix(1746000000, 0).UTC()
	if !got.ExpiresAt.Equal(want) {
		t.Fatalf("ExpiresAt=%v, want %v", got.ExpiresAt, want)
	}
}
