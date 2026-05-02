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

func TestBuildVLESSOutbound_WS(t *testing.T) {
	params := &VLESSParams{
		Address:   "example.com",
		Port:      443,
		UUID:      "uuid",
		SNI:       "sni.example.com",
		Security:  "tls",
		Type:      "ws",
		Path:      "/edge",
		Host:      []string{"cdn.example.com"},
		EarlyData: 2048,
	}
	out := buildVLESSOutbound(params)
	if out.Transport == nil {
		t.Fatal("Transport должен быть задан для ws")
	}
	want := &SBTransport{
		Type:                "ws",
		Path:                "/edge",
		Headers:             map[string]string{"Host": "cdn.example.com"},
		MaxEarlyData:        2048,
		EarlyDataHeaderName: "Sec-WebSocket-Protocol",
	}
	if !reflect.DeepEqual(out.Transport, want) {
		t.Fatalf("Transport = %#v, want %#v", out.Transport, want)
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

func TestBuildVLESSOutbound_GRPC(t *testing.T) {
	params := &VLESSParams{
		Address:     "example.com",
		Port:        443,
		UUID:        "uuid",
		SNI:         "sni.example.com",
		PublicKey:   "pub",
		ShortID:     "abc",
		Security:    "reality",
		Type:        "grpc",
		ServiceName: "GunService",
		GRPCMode:    "multi",
	}
	out := buildVLESSOutbound(params)
	if out.Transport == nil {
		t.Fatal("Transport должен быть задан для grpc")
	}
	want := &SBTransport{Type: "grpc", ServiceName: "GunService"}
	if !reflect.DeepEqual(out.Transport, want) {
		t.Fatalf("Transport = %#v, want %#v", out.Transport, want)
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

func TestBuildVLESSOutbound_HTTP(t *testing.T) {
	params := &VLESSParams{
		Address:  "example.com",
		Port:     443,
		UUID:     "uuid",
		SNI:      "sni.example.com",
		Security: "tls",
		Type:     "http",
		Path:     "/h2",
		Host:     []string{"a.example.com", "b.example.com"},
		ALPN:     []string{"h2"},
	}
	out := buildVLESSOutbound(params)
	if out.Transport == nil {
		t.Fatal("Transport должен быть задан для http")
	}
	want := &SBTransport{Type: "http", Host: []string{"a.example.com", "b.example.com"}, Path: "/h2", Method: "GET"}
	if !reflect.DeepEqual(out.Transport, want) {
		t.Fatalf("Transport = %#v, want %#v", out.Transport, want)
	}
	if out.TLS == nil || !reflect.DeepEqual(out.TLS.ALPN, []string{"h2"}) {
		t.Fatalf("HTTP transport должен сохранять ALPN h2: %+v", out.TLS)
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

func TestBuildVLESSOutbound_TCPHTTPObfuscation(t *testing.T) {
	params := &VLESSParams{
		Address:    "example.com",
		Port:       443,
		UUID:       "uuid",
		SNI:        "sni.example.com",
		Security:   "tls",
		Type:       "tcp",
		HeaderType: "http",
		Path:       "/",
		Host:       []string{"front.example.com"},
	}
	out := buildVLESSOutbound(params)
	if out.Transport == nil {
		t.Fatal("Transport должен быть задан для tcp headerType=http")
	}
	want := &SBTransport{Type: "http", Host: []string{"front.example.com"}, Path: "/", Method: "GET"}
	if !reflect.DeepEqual(out.Transport, want) {
		t.Fatalf("Transport = %#v, want %#v", out.Transport, want)
	}
}

func TestBuildVLESSOutbound_ALPNFromURL(t *testing.T) {
	params := &VLESSParams{
		Address:  "example.com",
		Port:     443,
		UUID:     "uuid",
		SNI:      "sni.example.com",
		Security: "tls",
		ALPN:     []string{"h2"},
	}
	out := buildVLESSOutbound(params)
	if out.TLS == nil {
		t.Fatal("TLS должен быть задан")
	}
	if !reflect.DeepEqual(out.TLS.ALPN, []string{"h2"}) {
		t.Fatalf("ALPN = %#v, want [h2]", out.TLS.ALPN)
	}
}

func TestBuildVLESSOutbound_Insecure(t *testing.T) {
	params := &VLESSParams{
		Address:  "example.com",
		Port:     443,
		UUID:     "uuid",
		SNI:      "sni.example.com",
		Security: "tls",
		Insecure: true,
	}
	out := buildVLESSOutbound(params)
	if out.TLS == nil || !out.TLS.Insecure {
		t.Fatalf("TLS.Insecure должен быть true: %+v", out.TLS)
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
