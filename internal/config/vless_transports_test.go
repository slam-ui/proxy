package config

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

type captureVLESSLogger struct {
	warnings []string
}

func (l *captureVLESSLogger) Debug(_ string, _ ...interface{}) {}
func (l *captureVLESSLogger) Info(_ string, _ ...interface{})  {}
func (l *captureVLESSLogger) Warn(format string, args ...interface{}) {
	if len(args) > 0 {
		l.warnings = append(l.warnings, fmt.Sprintf(format, args...))
		return
	}
	l.warnings = append(l.warnings, format)
}
func (l *captureVLESSLogger) Error(_ string, _ ...interface{}) {}

func (l *captureVLESSLogger) contains(substr string) bool {
	for _, warning := range l.warnings {
		if strings.Contains(warning, substr) {
			return true
		}
	}
	return false
}

func TestParseVLESSURL_BaseFields(t *testing.T) {
	log := &captureVLESSLogger{}
	restore := setVLESSLoggerForTest(log)
	defer restore()

	p, err := ParseVLESSContent("vless://uuid@example.com:443?encryption=none&security=tls&sni=sni.example.com&alpn=h2,http/1.1&insecure=true")
	if err != nil {
		t.Fatalf("ParseVLESSContent: %v", err)
	}
	if p.Encryption != "none" {
		t.Fatalf("Encryption = %q, want none", p.Encryption)
	}
	if p.Security != "tls" {
		t.Fatalf("Security = %q, want tls", p.Security)
	}
	if !reflect.DeepEqual(p.ALPN, []string{"h2", "http/1.1"}) {
		t.Fatalf("ALPN = %#v", p.ALPN)
	}
	if !p.Insecure {
		t.Fatal("Insecure должен быть true")
	}
	if !log.contains("TLS-проверка сертификата отключена") {
		t.Fatalf("ожидали warning про insecure, got %#v", log.warnings)
	}
}

func TestParseVLESSURL_WS(t *testing.T) {
	p, err := ParseVLESSContent("vless://uuid@example.com:443?security=tls&type=ws&path=%2Fedge%3Fed%3D2048&host=cdn.example.com")
	if err != nil {
		t.Fatalf("ParseVLESSContent: %v", err)
	}
	if p.Type != "ws" {
		t.Fatalf("Type = %q, want ws", p.Type)
	}
	if p.Path != "/edge" {
		t.Fatalf("Path = %q, want /edge", p.Path)
	}
	if p.EarlyData != 2048 {
		t.Fatalf("EarlyData = %d, want 2048", p.EarlyData)
	}
	if !reflect.DeepEqual(p.Host, []string{"cdn.example.com"}) {
		t.Fatalf("Host = %#v", p.Host)
	}
}

func TestParseVLESSURL_GRPC(t *testing.T) {
	log := &captureVLESSLogger{}
	restore := setVLESSLoggerForTest(log)
	defer restore()

	p, err := ParseVLESSContent("vless://uuid@example.com:443?security=reality&type=grpc&serviceName=GunService&mode=multi&sni=www.microsoft.com&pbk=pub&sid=abc")
	if err != nil {
		t.Fatalf("ParseVLESSContent: %v", err)
	}
	if p.Type != "grpc" || p.ServiceName != "GunService" || p.GRPCMode != "multi" {
		t.Fatalf("grpc params not parsed: %+v", p)
	}
	if !log.contains("mode=multi") {
		t.Fatalf("ожидали warning про grpc mode, got %#v", log.warnings)
	}
}

func TestParseVLESSURL_HTTP(t *testing.T) {
	p, err := ParseVLESSContent("vless://uuid@example.com:443?security=tls&type=h2&path=/h2&host=a.example.com,b.example.com")
	if err != nil {
		t.Fatalf("ParseVLESSContent: %v", err)
	}
	if p.Type != "http" {
		t.Fatalf("Type = %q, want http", p.Type)
	}
	if p.Path != "/h2" {
		t.Fatalf("Path = %q, want /h2", p.Path)
	}
	if !reflect.DeepEqual(p.Host, []string{"a.example.com", "b.example.com"}) {
		t.Fatalf("Host = %#v", p.Host)
	}
}

func TestParseVLESSURL_HTTPUpgrade(t *testing.T) {
	p, err := ParseVLESSContent("vless://uuid@example.com:443?security=tls&type=httpupgrade&path=/up&host=edge.example.com")
	if err != nil {
		t.Fatalf("ParseVLESSContent: %v", err)
	}
	if p.Type != "httpupgrade" || p.Path != "/up" {
		t.Fatalf("httpupgrade params not parsed: %+v", p)
	}
	if !reflect.DeepEqual(p.Host, []string{"edge.example.com"}) {
		t.Fatalf("Host = %#v", p.Host)
	}
}

func TestParseVLESSURL_TCPHTTPObfuscation(t *testing.T) {
	p, err := ParseVLESSContent("vless://uuid@example.com:443?security=tls&type=tcp&headerType=http&path=/&host=front.example.com")
	if err != nil {
		t.Fatalf("ParseVLESSContent: %v", err)
	}
	if p.Type != "tcp" || p.HeaderType != "http" || p.Path != "/" {
		t.Fatalf("tcp http-obf params not parsed: %+v", p)
	}
	if !reflect.DeepEqual(p.Host, []string{"front.example.com"}) {
		t.Fatalf("Host = %#v", p.Host)
	}
}

func TestParseVLESSURL_RejectsUnsupportedTransports(t *testing.T) {
	for _, typ := range []string{"quic", "kcp", "mkcp"} {
		t.Run(typ, func(t *testing.T) {
			_, err := ParseVLESSContent("vless://uuid@example.com:443?security=tls&type=" + typ)
			if err == nil {
				t.Fatalf("ожидали ошибку для type=%s", typ)
			}
			if !strings.Contains(err.Error(), "не поддерживается sing-box") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestParseVLESSURL_RejectsGRPCWithoutServiceName(t *testing.T) {
	_, err := ParseVLESSContent("vless://uuid@example.com:443?security=tls&type=grpc")
	if err == nil {
		t.Fatal("ожидали ошибку для grpc без serviceName")
	}
	if !strings.Contains(err.Error(), "serviceName") {
		t.Fatalf("error = %v", err)
	}
}

func TestParseVLESSURL_RejectsInvalidEncryption(t *testing.T) {
	_, err := ParseVLESSContent("vless://uuid@example.com:443?security=tls&encryption=auto")
	if err == nil {
		t.Fatal("ожидали ошибку для encryption=auto")
	}
	if !strings.Contains(err.Error(), "encryption=none") {
		t.Fatalf("error = %v", err)
	}
}

func TestParseVLESSURL_RejectsInvalidHeaderType(t *testing.T) {
	_, err := ParseVLESSContent("vless://uuid@example.com:443?security=tls&type=tcp&headerType=srtp")
	if err == nil {
		t.Fatal("ожидали ошибку для headerType=srtp")
	}
	if !strings.Contains(err.Error(), "headerType=srtp") {
		t.Fatalf("error = %v", err)
	}
}

func TestParseVLESSURL_WarnsOnSPX(t *testing.T) {
	log := &captureVLESSLogger{}
	restore := setVLESSLoggerForTest(log)
	defer restore()

	_, err := ParseVLESSContent("vless://uuid@example.com:443?security=reality&sni=sni.example.com&pbk=pub&sid=abc&spx=/")
	if err != nil {
		t.Fatalf("ParseVLESSContent: %v", err)
	}
	if !log.contains("spx") {
		t.Fatalf("ожидали warning про spx, got %#v", log.warnings)
	}
}
