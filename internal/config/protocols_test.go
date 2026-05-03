package config

import (
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"testing"

	"proxyclient/internal/mtu"
)

func TestParseServerContentTrojanWS(t *testing.T) {
	got, err := ParseServerContent("trojan://secret@example.com:443?security=tls&type=ws&path=%2Fws&host=cdn.example.com&sni=edge.example.com#edge")
	if err != nil {
		t.Fatalf("ParseServerContent: %v", err)
	}
	if got.Proto != "trojan" || got.Outbound.Type != "trojan" {
		t.Fatalf("unexpected proto/outbound: %+v", got)
	}
	if got.Outbound.Password != "secret" || got.Outbound.TLS.ServerName != "edge.example.com" {
		t.Fatalf("unexpected outbound: %+v", got.Outbound)
	}
	if got.Outbound.Transport == nil || got.Outbound.Transport.Type != "ws" || got.Outbound.Transport.Path != "/ws" {
		t.Fatalf("unexpected transport: %+v", got.Outbound.Transport)
	}
}

func TestParseServerContentTrojanRejectsReality(t *testing.T) {
	if _, err := ParseServerContent("trojan://secret@example.com:443?security=reality&pbk=abc"); err == nil {
		t.Fatal("accepted trojan reality")
	}
}

func TestParseServerContentShadowsocksSIP002(t *testing.T) {
	user := base64.RawURLEncoding.EncodeToString([]byte("2022-blake3-aes-128-gcm:pass"))
	got, err := ParseServerContent("ss://" + user + "@example.com:8388#ss")
	if err != nil {
		t.Fatalf("ParseServerContent: %v", err)
	}
	if got.Proto != "shadowsocks" || got.Outbound.Type != "shadowsocks" {
		t.Fatalf("unexpected proto/outbound: %+v", got)
	}
	if got.Outbound.Method != "2022-blake3-aes-128-gcm" || got.Outbound.Password != "pass" {
		t.Fatalf("unexpected ss outbound: %+v", got.Outbound)
	}
}

func TestParseServerContentShadowsocksRejectsLegacyCipher(t *testing.T) {
	user := base64.RawStdEncoding.EncodeToString([]byte("rc4-md5:pass"))
	if _, err := ParseServerContent("ss://" + user + "@example.com:8388"); err == nil {
		t.Fatal("accepted unsupported cipher")
	}
}

func TestParseServerContentVLESSStillWorks(t *testing.T) {
	raw := "vless://00000000-0000-0000-0000-000000000000@example.com:443?security=tls&sni=example.com&type=tcp"
	got, err := ParseServerContent(raw)
	if err != nil {
		t.Fatalf("ParseServerContent: %v", err)
	}
	if got.Proto != "vless" || got.Outbound.Type != "vless" {
		t.Fatalf("unexpected vless result: %+v", got)
	}
}

func TestParseServerContentHysteria2(t *testing.T) {
	got, err := ParseServerContent("hysteria2://pass@example.com:443?sni=edge.example.com&obfs=salamander&obfs-password=obfs&up=80&down=240#hy2")
	if err != nil {
		t.Fatalf("ParseServerContent: %v", err)
	}
	if got.Proto != "hysteria2" || got.Outbound.Type != "hysteria2" {
		t.Fatalf("unexpected result: %+v", got)
	}
	if got.Outbound.TLS == nil || got.Outbound.TLS.ServerName != "edge.example.com" || got.Outbound.TLS.ALPN[0] != "h3" {
		t.Fatalf("unexpected tls: %+v", got.Outbound.TLS)
	}
	if got.Outbound.Obfs == nil || got.Outbound.Obfs.Password != "obfs" {
		t.Fatalf("unexpected obfs: %+v", got.Outbound.Obfs)
	}
	if got.Outbound.UpMbps != 80 || got.Outbound.DownMbps != 240 || got.Outbound.MTU != 1200 {
		t.Fatalf("unexpected bandwidth/mtu: %+v", got.Outbound)
	}
}

func TestParseServerContentTUIC(t *testing.T) {
	got, err := ParseServerContent("tuic://00000000-0000-0000-0000-000000000000:pass@example.com:443?sni=edge.example.com&congestion_control=cubic&udp_relay_mode=quic#tuic")
	if err != nil {
		t.Fatalf("ParseServerContent: %v", err)
	}
	if got.Proto != "tuic" || got.Outbound.Type != "tuic" {
		t.Fatalf("unexpected result: %+v", got)
	}
	if got.Outbound.UUID == "" || got.Outbound.Password != "pass" {
		t.Fatalf("unexpected auth: %+v", got.Outbound)
	}
	if got.Outbound.CongestionControl != "cubic" || got.Outbound.UDPRelayMode != "quic" {
		t.Fatalf("unexpected tuic settings: %+v", got.Outbound)
	}
}

func TestParseServerContentTUICRejectsBadCongestion(t *testing.T) {
	if _, err := ParseServerContent("tuic://id:pass@example.com:443?congestion_control=bad"); err == nil {
		t.Fatal("accepted bad congestion_control")
	}
}

func TestParseServerContentWireGuardConf(t *testing.T) {
	conf := `[Interface]
PrivateKey = priv
Address = 10.0.0.2/32, fd00::2/128
DNS = 1.1.1.1
MTU = 1420

[Peer]
PublicKey = pub
PresharedKey = psk
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = wg.example.com:51820
PersistentKeepalive = 25
`
	got, err := ParseServerContent(conf)
	if err != nil {
		t.Fatalf("ParseServerContent: %v", err)
	}
	if got.Proto != "wireguard" || got.Outbound.Type != "wireguard" {
		t.Fatalf("unexpected result: %+v", got)
	}
	if got.Outbound.PrivateKey != "priv" || got.Outbound.PeerPublicKey != "pub" || got.Outbound.PreSharedKey != "psk" {
		t.Fatalf("unexpected keys: %+v", got.Outbound)
	}
	if got.Outbound.MTU != 1420 || len(got.Outbound.LocalAddress) != 2 {
		t.Fatalf("unexpected wg settings: %+v", got.Outbound)
	}
}

func TestParseServerContentWireGuardURL(t *testing.T) {
	got, err := ParseServerContent("wireguard://priv@wg.example.com:51820?publickey=pub&address=10.0.0.2%2F32&mtu=1280#WG")
	if err != nil {
		t.Fatalf("ParseServerContent: %v", err)
	}
	if got.Outbound.Server != "wg.example.com" || got.Outbound.ServerPort != 51820 {
		t.Fatalf("unexpected endpoint: %+v", got.Outbound)
	}
	if got.Outbound.MTU != 1280 || got.Outbound.LocalAddress[0] != "10.0.0.2/32" {
		t.Fatalf("unexpected wg outbound: %+v", got.Outbound)
	}
}

func TestParseServerContentWireGuardUsesCachedMTUWhenMissing(t *testing.T) {
	oldPath := wireGuardMTUCachePath
	wireGuardMTUCachePath = filepath.Join(t.TempDir(), "mtu_cache.json")
	t.Cleanup(func() { wireGuardMTUCachePath = oldPath })
	if err := mtu.NewCache(wireGuardMTUCachePath, 0).Store("wg.example.com", 51820, 1420); err != nil {
		t.Fatalf("Store MTU cache: %v", err)
	}

	got, err := ParseServerContent("wireguard://priv@wg.example.com:51820?publickey=pub&address=10.0.0.2%2F32#WG")
	if err != nil {
		t.Fatalf("ParseServerContent: %v", err)
	}
	if got.Outbound.MTU != 1420 {
		t.Fatalf("WireGuard MTU = %d, want cached 1420", got.Outbound.MTU)
	}
}

func TestParseServerContentWireGuardClampsExplicitMTU(t *testing.T) {
	got, err := ParseServerContent("wireguard://priv@wg.example.com:51820?publickey=pub&address=10.0.0.2%2F32&mtu=9000#WG")
	if err != nil {
		t.Fatalf("ParseServerContent: %v", err)
	}
	if got.Outbound.MTU != mtu.MaxMTU {
		t.Fatalf("WireGuard MTU = %d, want %d", got.Outbound.MTU, mtu.MaxMTU)
	}
}

func TestParseServerContentVMessWS(t *testing.T) {
	payload, _ := json.Marshal(map[string]string{
		"v":    "2",
		"ps":   "vmess-ws",
		"add":  "example.com",
		"port": "443",
		"id":   "00000000-0000-0000-0000-000000000000",
		"aid":  "0",
		"scy":  "auto",
		"net":  "ws",
		"type": "none",
		"host": "cdn.example.com",
		"path": "/ws",
		"tls":  "tls",
		"sni":  "edge.example.com",
	})
	got, err := ParseServerContent("vmess://" + base64.RawStdEncoding.EncodeToString(payload))
	if err != nil {
		t.Fatalf("ParseServerContent: %v", err)
	}
	if got.Proto != "vmess" || got.Outbound.Type != "vmess" || got.Outbound.Security != "auto" {
		t.Fatalf("unexpected vmess result: %+v", got)
	}
	if got.Outbound.Transport == nil || got.Outbound.Transport.Type != "ws" {
		t.Fatalf("unexpected transport: %+v", got.Outbound.Transport)
	}
}

func TestParseServerContentVMessRejectsAlterID(t *testing.T) {
	payload, _ := json.Marshal(map[string]string{
		"add": "example.com", "port": "443", "id": "00000000-0000-0000-0000-000000000000", "aid": "1", "net": "ws",
	})
	if _, err := ParseServerContent("vmess://" + base64.StdEncoding.EncodeToString(payload)); err == nil {
		t.Fatal("accepted alterId > 0")
	}
}
