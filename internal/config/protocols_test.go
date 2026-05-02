package config

import (
	"encoding/base64"
	"testing"
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
