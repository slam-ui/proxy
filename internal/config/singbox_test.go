package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildRoute — внутренняя функция, доступна из пакета config (same package test)

// ─── buildRoute: распределение правил по outbound'ам ──────────────────────

func TestBuildRoute_DefaultProxy(t *testing.T) {
	cfg := &RoutingConfig{DefaultAction: ActionProxy, Rules: []RoutingRule{}}
	route := buildRoute(cfg, "1.2.3.4")
	if route.Final != "proxy-out" {
		t.Errorf("Final = %q, want proxy-out", route.Final)
	}
}

func TestBuildRoute_DefaultDirect(t *testing.T) {
	cfg := &RoutingConfig{DefaultAction: ActionDirect, Rules: []RoutingRule{}}
	route := buildRoute(cfg, "1.2.3.4")
	if route.Final != "direct" {
		t.Errorf("Final = %q, want direct", route.Final)
	}
}

func TestBuildRoute_DefaultBlock(t *testing.T) {
	cfg := &RoutingConfig{DefaultAction: ActionBlock, Rules: []RoutingRule{}}
	route := buildRoute(cfg, "1.2.3.4")
	if route.Final != "block" {
		t.Errorf("Final = %q, want block", route.Final)
	}
}

func TestBuildRoute_DomainGoesToCorrectOutbound(t *testing.T) {
	cfg := &RoutingConfig{
		DefaultAction: ActionDirect,
		Rules: []RoutingRule{
			{Value: "youtube.com", Type: RuleTypeDomain, Action: ActionProxy},
			{Value: "local.corp", Type: RuleTypeDomain, Action: ActionDirect},
			{Value: "ads.com", Type: RuleTypeDomain, Action: ActionBlock},
		},
	}
	route := buildRoute(cfg, "1.2.3.4")

	// Проверяем что у нас есть правила для каждого outbound
	outbounds := map[string]bool{}
	for _, r := range route.Rules {
		outbounds[r.Outbound] = true
		if r.Action == "reject" {
			outbounds["block"] = true
		}
	}

	if !outbounds["proxy-out"] {
		t.Error("ожидали правило для proxy-out")
	}
	if !outbounds["direct"] {
		t.Error("ожидали правило для direct")
	}
	if !outbounds["block"] {
		t.Error("ожидали правило-reject для block")
	}
}

func TestBuildRoute_ProcessRule(t *testing.T) {
	cfg := &RoutingConfig{
		DefaultAction: ActionDirect,
		Rules: []RoutingRule{
			{Value: "chrome.exe", Type: RuleTypeProcess, Action: ActionProxy},
		},
	}
	route := buildRoute(cfg, "1.2.3.4")

	var found bool
	for _, r := range route.Rules {
		if len(r.ProcessName) > 0 && r.Outbound == "proxy-out" {
			for _, p := range r.ProcessName {
				if p == "chrome.exe" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Error("chrome.exe не попал в proxy-out process rule")
	}
}

func TestBuildRoute_IPCIDRRule(t *testing.T) {
	cfg := &RoutingConfig{
		DefaultAction: ActionDirect,
		Rules: []RoutingRule{
			{Value: "10.0.0.0/8", Type: RuleTypeIP, Action: ActionProxy},
		},
	}
	route := buildRoute(cfg, "1.2.3.4")

	var found bool
	for _, r := range route.Rules {
		for _, ip := range r.IPCIDR {
			if ip == "10.0.0.0/8" {
				found = true
			}
		}
	}
	if !found {
		t.Error("CIDR 10.0.0.0/8 не попал в маршрут")
	}
}

func TestBuildRoute_AutoDetectInterface(t *testing.T) {
	route := buildRoute(DefaultRoutingConfig(), "1.2.3.4")
	// AutoDetectInterface=true: обязателен при auto_route=true.
	// Без этого direct-трафик уходит обратно в TUN вместо физического интерфейса → петля.
	if !route.AutoDetectInterface {
		t.Error("AutoDetectInterface должен быть true при auto_route=true")
	}
}

func TestBuildRoute_EmptyRules_NoRuleSetGenerated(t *testing.T) {
	cfg := &RoutingConfig{DefaultAction: ActionProxy, Rules: []RoutingRule{}}
	route := buildRoute(cfg, "1.2.3.4")
	if len(route.RuleSet) != 0 {
		t.Errorf("RuleSet должен быть пустым при отсутствии geosite правил, got %d", len(route.RuleSet))
	}
}

// ─── GenerateSingBoxConfig: ошибка при отсутствии geosite файла ───────────

func TestGenerateSingBoxConfig_MissingGeositeFile_ReturnsError(t *testing.T) {
	dir := t.TempDir()

	// Создаём валидный secret.key
	secretPath := filepath.Join(dir, "secret.key")
	os.WriteFile(secretPath, []byte(
		"vless://12345678-1234-1234-1234-123456789abc@example.com:443?sni=www.google.com&pbk=testkey&sid=abc",
	), 0644)

	outputPath := filepath.Join(dir, "out.json")

	// Правило ссылается на geosite, файл которого не скачан
	cfg := &RoutingConfig{
		DefaultAction: ActionProxy,
		Rules: []RoutingRule{
			{Value: "geosite:youtube", Type: RuleTypeGeosite, Action: ActionProxy},
		},
	}

	// Меняем CWD в dir чтобы GenerateSingBoxConfig искал .bin там
	old, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(old)

	err := GenerateSingBoxConfig(secretPath, outputPath, cfg)
	if err == nil {
		t.Fatal("ожидали ошибку — geosite-youtube.bin не существует")
	}
	if !strings.Contains(err.Error(), "geosite") {
		t.Errorf("ошибка должна упоминать geosite, got: %v", err)
	}
}

func TestGenerateSingBoxConfig_MissingSecretKey_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	err := GenerateSingBoxConfig(
		filepath.Join(dir, "nonexistent.key"),
		filepath.Join(dir, "out.json"),
		DefaultRoutingConfig(),
	)
	if err == nil {
		t.Fatal("ожидали ошибку для несуществующего secret.key")
	}
}

func TestGenerateSingBoxConfig_InvalidVLESS_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "bad.key")
	os.WriteFile(secretPath, []byte("not-a-vless-url"), 0644)

	err := GenerateSingBoxConfig(secretPath, filepath.Join(dir, "out.json"), DefaultRoutingConfig())
	if err == nil {
		t.Fatal("ожидали ошибку для невалидного VLESS URL")
	}
}

// ─── parseVLESSKey ─────────────────────────────────────────────────────────

func TestParseVLESSKey_Valid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.key")
	url := "vless://12345678-1234-1234-1234-123456789abc@example.com:443?sni=www.google.com&pbk=mypubkey&sid=abc123"
	os.WriteFile(path, []byte(url), 0644)

	p, err := parseVLESSKey(path)
	if err != nil {
		t.Fatalf("parseVLESSKey failed: %v", err)
	}
	if p.Address != "example.com" {
		t.Errorf("Address = %q", p.Address)
	}
	if p.Port != 443 {
		t.Errorf("Port = %d", p.Port)
	}
	if p.UUID != "12345678-1234-1234-1234-123456789abc" {
		t.Errorf("UUID = %q", p.UUID)
	}
	if p.SNI != "www.google.com" {
		t.Errorf("SNI = %q", p.SNI)
	}
	if p.PublicKey != "mypubkey" {
		t.Errorf("PublicKey = %q", p.PublicKey)
	}
	if p.ShortID != "abc123" {
		t.Errorf("ShortID = %q", p.ShortID)
	}
}

func TestParseVLESSKey_WithBOM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bom.key")
	url := "vless://uuid@host.com:443?sni=x.com&pbk=k&sid=s"
	// BOM + URL — должен быть стриппирован
	os.WriteFile(path, append([]byte{0xEF, 0xBB, 0xBF}, []byte(url)...), 0644)

	_, err := parseVLESSKey(path)
	if err != nil {
		t.Errorf("parseVLESSKey с BOM вернул ошибку: %v", err)
	}
}

func TestParseVLESSKey_WrongProtocol_ReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.key")
	os.WriteFile(path, []byte("http://example.com:443"), 0644)
	_, err := parseVLESSKey(path)
	if err == nil {
		t.Error("ожидали ошибку для non-vless URL")
	}
}

func TestParseVLESSKey_Empty_ReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.key")
	os.WriteFile(path, []byte(""), 0644)
	_, err := parseVLESSKey(path)
	if err == nil {
		t.Error("ожидали ошибку для пустого файла")
	}
}

func TestParseVLESSKey_FileNotFound_ReturnsError(t *testing.T) {
	_, err := parseVLESSKey("/nonexistent/secret.key")
	if err == nil {
		t.Error("ожидали ошибку для несуществующего файла")
	}
}

// ─── validateVLESSParams ───────────────────────────────────────────────────

func TestValidateVLESSParams(t *testing.T) {
	valid := &VLESSParams{
		Address: "example.com", Port: 443,
		UUID: "uuid", SNI: "sni", PublicKey: "key", ShortID: "id",
	}

	cases := []struct {
		name    string
		modify  func(*VLESSParams)
		wantErr bool
	}{
		{"valid", func(p *VLESSParams) {}, false},
		{"missing address", func(p *VLESSParams) { p.Address = "" }, true},
		{"missing UUID", func(p *VLESSParams) { p.UUID = "" }, true},
		{"missing SNI", func(p *VLESSParams) { p.SNI = "" }, true},
		// missing PublicKey → plain TLS режим (нет Reality) → ошибки нет.
		// BUG FIX: раньше PublicKey был обязателен безусловно, что ломало
		// подключение к серверам без Reality. Теперь пустой PublicKey = plain TLS.
		{"missing PublicKey", func(p *VLESSParams) { p.PublicKey = "" }, false},
		{"missing ShortID", func(p *VLESSParams) { p.ShortID = "" }, true},
		{"port 0", func(p *VLESSParams) { p.Port = 0 }, true},
		{"port 70000", func(p *VLESSParams) { p.Port = 70000 }, true},
		{"port 1", func(p *VLESSParams) { p.Port = 1 }, false},
		{"port 65535", func(p *VLESSParams) { p.Port = 65535 }, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// копируем чтобы не мутировать valid
			p := *valid
			tc.modify(&p)
			err := validateVLESSParams(&p)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateVLESSParams() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

// ─── LAN bypass: privateIPRanges always go direct ────────────────────────

func TestBuildRoute_PrivateIPBypass_AlwaysDirect(t *testing.T) {
	// Even with default=proxy and no explicit rules,
	// private IP ranges must have a direct rule.
	cfg := &RoutingConfig{DefaultAction: ActionProxy, Rules: []RoutingRule{}}
	route := buildRoute(cfg, "1.2.3.4")

	// Collect all IPCIDR entries that go to "direct"
	var directCIDRs []string
	for _, r := range route.Rules {
		if r.Outbound == "direct" {
			directCIDRs = append(directCIDRs, r.IPCIDR...)
		}
	}

	wants := []string{"192.168.0.0/16", "10.0.0.0/8", "127.0.0.0/8"}
	for _, want := range wants {
		found := false
		for _, cidr := range directCIDRs {
			if cidr == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("private CIDR %q not in direct rules (got: %v)", want, directCIDRs)
		}
	}
}

func TestBuildRoute_PrivateIPBypass_BeforeUserRules(t *testing.T) {
	// The LAN direct rule must appear BEFORE geosite/domain rules
	// so it takes priority and can't be overridden by a proxy rule for 192.168.x.x
	cfg := &RoutingConfig{
		DefaultAction: ActionProxy,
		Rules: []RoutingRule{
			{Value: "youtube.com", Type: RuleTypeDomain, Action: ActionProxy},
		},
	}
	route := buildRoute(cfg, "1.2.3.4")

	// First non-DNS rule should be the private IP bypass
	firstReal := -1
	for i, r := range route.Rules {
		if r.Protocol != "dns" {
			firstReal = i
			break
		}
	}
	if firstReal < 0 {
		t.Fatal("no non-DNS rules found")
	}
	r := route.Rules[firstReal]
	if r.Outbound != "direct" || len(r.IPCIDR) == 0 {
		t.Errorf("first rule after DNS should be LAN direct bypass, got outbound=%q IPCIDR=%v",
			r.Outbound, r.IPCIDR)
	}
}
