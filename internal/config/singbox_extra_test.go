package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── buildRoute: domain-suffix правила ─────────────────────────────────────

// BUG-РИСК: правило с доменом начинающимся на "." должно попасть в
// DomainSuffix, а не Domain. Без этого ".youtube.com" не будет матчить
// "www.youtube.com" в sing-box.
func TestBuildRoute_DomainSuffix_AddedToDomainSuffix(t *testing.T) {
	cfg := &RoutingConfig{
		DefaultAction: ActionDirect,
		Rules: []RoutingRule{
			{Value: ".youtube.com", Type: RuleTypeDomain, Action: ActionProxy},
		},
	}
	route := buildRoute(cfg, "1.2.3.4")

	var found bool
	for _, r := range route.Rules {
		if r.Outbound == "proxy-out" {
			for _, s := range r.DomainSuffix {
				if s == "youtube.com" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Error(".youtube.com должен попасть в DomainSuffix с stripped-точкой, а не в Domain")
	}
}

// BUG-РИСК: без этого теста домен без точки мог случайно уйти в DomainSuffix.
func TestBuildRoute_PlainDomain_NotInDomainSuffix(t *testing.T) {
	cfg := &RoutingConfig{
		DefaultAction: ActionDirect,
		Rules: []RoutingRule{
			{Value: "youtube.com", Type: RuleTypeDomain, Action: ActionProxy},
		},
	}
	route := buildRoute(cfg, "1.2.3.4")

	for _, r := range route.Rules {
		if r.Outbound == "proxy-out" {
			for _, s := range r.DomainSuffix {
				if s == "youtube.com" {
					t.Error("youtube.com (без точки) НЕ должен попасть в DomainSuffix")
				}
			}
		}
	}
}

// ── buildRoute: serverAddr всегда в directCIDR ────────────────────────────

// BUG-РИСК: если serverAddr не попал в directCIDR — sing-box замаршрутизирует
// собственные пакеты снова в TUN → routing loop → нет интернета.
func TestBuildRoute_ServerAddr_InDirectCIDR(t *testing.T) {
	serverAddr := "5.6.7.8"
	cfg := &RoutingConfig{DefaultAction: ActionProxy, Rules: []RoutingRule{}}
	route := buildRoute(cfg, serverAddr)

	want := serverAddr + "/32"
	for _, r := range route.Rules {
		if r.Outbound == "direct" {
			for _, cidr := range r.IPCIDR {
				if cidr == want {
					return // OK
				}
			}
		}
	}
	t.Errorf("serverAddr %q/32 не найден в direct IPCIDR (routing loop!)", serverAddr)
}

// ── buildRoute: IPv6 private ranges тоже direct ───────────────────────────

// BUG-РИСК: ::1 и fe80::/10 должны быть direct, иначе loopback-IPv6 уходит
// в прокси-туннель и DNS ломается в некоторых конфигурациях Windows.
func TestBuildRoute_IPv6PrivateRanges_AreDirect(t *testing.T) {
	cfg := &RoutingConfig{DefaultAction: ActionProxy, Rules: []RoutingRule{}}
	route := buildRoute(cfg, "1.2.3.4")

	var allDirectCIDRs []string
	for _, r := range route.Rules {
		if r.Outbound == "direct" {
			allDirectCIDRs = append(allDirectCIDRs, r.IPCIDR...)
		}
	}

	ipv6Ranges := []string{"::1/128", "fc00::/7", "fe80::/10"}
	for _, want := range ipv6Ranges {
		found := false
		for _, cidr := range allDirectCIDRs {
			if cidr == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("IPv6 диапазон %q не в direct rules (IPv6 DNS leak!)", want)
		}
	}
}

// ── buildRoute: link-local 169.254/16 direct ─────────────────────────────

func TestBuildRoute_LinkLocal169_IsDirect(t *testing.T) {
	cfg := &RoutingConfig{DefaultAction: ActionProxy, Rules: []RoutingRule{}}
	route := buildRoute(cfg, "1.2.3.4")

	for _, r := range route.Rules {
		if r.Outbound == "direct" {
			for _, cidr := range r.IPCIDR {
				if cidr == "169.254.0.0/16" {
					return // OK
				}
			}
		}
	}
	t.Error("169.254.0.0/16 (link-local) не в direct rules — DHCP/APIPA сломается")
}

// ── buildRoute: DNS hijack — первое правило ───────────────────────────────

// BUG-РИСК: правило hijack-dns должно идти ПЕРВЫМ, иначе DNS-трафик может
// смаршрутизироваться в direct/proxy до того как sing-box его перехватит.
func TestBuildRoute_DNSHijack_IsFirstRule(t *testing.T) {
	cfg := &RoutingConfig{DefaultAction: ActionProxy, Rules: []RoutingRule{}}
	route := buildRoute(cfg, "1.2.3.4")

	if len(route.Rules) == 0 {
		t.Fatal("правила маршрутизации пусты")
	}
	first := route.Rules[0]
	if first.Protocol != "dns" {
		t.Errorf("первое правило должно быть dns-hijack, got Protocol=%q Outbound=%q",
			first.Protocol, first.Outbound)
	}
}

// ── buildRoute: Final правильно отражает DefaultAction ───────────────────

func TestBuildRoute_Final_ReflectsDefaultAction(t *testing.T) {
	cases := []struct {
		action RuleAction
		want   string
	}{
		{ActionProxy, "proxy-out"},
		{ActionDirect, "direct"},
		{ActionBlock, "block"},
	}
	for _, tc := range cases {
		cfg := &RoutingConfig{DefaultAction: tc.action}
		route := buildRoute(cfg, "1.2.3.4")
		if route.Final != tc.want {
			t.Errorf("DefaultAction=%q → Final=%q, want %q", tc.action, route.Final, tc.want)
		}
	}
}

// ── NormalizeRuleValue: специфические случаи ─────────────────────────────

// BUG-РИСК: пользователь вставляет URL из браузера — нужно корректно стриппить.
func TestNormalizeRuleValue_URLWithPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://youtube.com/watch?v=abc", "youtube.com"},
		{"http://example.com:8080/path", "example.com"},
		{"https://sub.domain.co.uk/page", "sub.domain.co.uk"},
	}
	for _, tc := range cases {
		got := NormalizeRuleValue(tc.in)
		if got != tc.want {
			t.Errorf("NormalizeRuleValue(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// BUG-РИСК: CIDR-правило не должно терять слеш.
func TestNormalizeRuleValue_CIDRPreserved(t *testing.T) {
	cases := []struct{ in, want string }{
		{"192.168.1.0/24", "192.168.1.0/24"},
		{"10.0.0.0/8", "10.0.0.0/8"},
	}
	for _, tc := range cases {
		got := NormalizeRuleValue(tc.in)
		if got != tc.want {
			t.Errorf("NormalizeRuleValue(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// BUG-РИСК: whitespace-only строки не должны содержать пробелы в результате.
func TestNormalizeRuleValue_WhitespaceOnly(t *testing.T) {
	cases := []string{"   ", "\t", "\r\n", ""}
	for _, in := range cases {
		got := NormalizeRuleValue(in)
		if strings.ContainsAny(got, " \t\r\n") {
			t.Errorf("NormalizeRuleValue(%q) = %q содержит пробелы", in, got)
		}
	}
}

// ── SanitizeRoutingConfig: дублирующиеся значения ────────────────────────

// BUG-РИСК: Sanitize не должна молча удалять правила.
func TestSanitizeRoutingConfig_PreservesAllRules(t *testing.T) {
	cfg := &RoutingConfig{
		DefaultAction: ActionProxy,
		Rules: []RoutingRule{
			{Value: "youtube.com", Type: RuleTypeDomain, Action: ActionProxy},
			{Value: "youtube.com", Type: RuleTypeDomain, Action: ActionDirect},
			{Value: "telegram.exe", Type: RuleTypeProcess, Action: ActionDirect},
		},
	}
	SanitizeRoutingConfig(cfg)
	if len(cfg.Rules) != 3 {
		t.Errorf("SanitizeRoutingConfig удалила правила: len=%d, want 3", len(cfg.Rules))
	}
}

// BUG-РИСК: невалидный DefaultAction не должен вызывать панику.
func TestSanitizeRoutingConfig_InvalidDefaultAction_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("SanitizeRoutingConfig с невалидным DefaultAction вызвал panic: %v", r)
		}
	}()
	cfg := &RoutingConfig{
		DefaultAction: "INVALID_ACTION",
		Rules:         []RoutingRule{},
	}
	SanitizeRoutingConfig(cfg)
}

// ── GenerateSingBoxConfig: nil routing не паникует ───────────────────────

// BUG-РИСК: GenerateSingBoxConfig(path, out, nil) должен использовать
// DefaultRoutingConfig, а не упасть с nil pointer dereference.
func TestGenerateSingBoxConfig_NilRouting_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("GenerateSingBoxConfig с nil routing вызвал panic: %v", r)
		}
	}()

	dir := t.TempDir()
	secretPath := filepath.Join(dir, "secret.key")
	os.WriteFile(secretPath, []byte(
		"vless://12345678-1234-1234-1234-123456789abc@example.com:443?sni=www.google.com&pbk=testkey&sid=abc",
	), 0644)

	old, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(old)

	outputPath := filepath.Join(dir, "out.json")
	// nil routing должен работать без паники
	_ = GenerateSingBoxConfig(secretPath, outputPath, nil)
}

// ── GenerateSingBoxConfig: выходной JSON валиден ─────────────────────────

// BUG-РИСК: атомарная запись или сериализация не должна обрезать файл.
func TestGenerateSingBoxConfig_OutputIsValidJSON(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "secret.key")
	os.WriteFile(secretPath, []byte(
		"vless://12345678-1234-1234-1234-123456789abc@example.com:443?sni=www.google.com&pbk=testkey&sid=abc",
	), 0644)

	old, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(old)

	outputPath := filepath.Join(dir, "out.json")
	cfg := &RoutingConfig{DefaultAction: ActionProxy, Rules: []RoutingRule{}}
	err := GenerateSingBoxConfig(secretPath, outputPath, cfg)
	if err != nil {
		t.Skipf("GenerateSingBoxConfig вернул ошибку (нет geosite): %v", err)
	}

	data, readErr := os.ReadFile(outputPath)
	if readErr != nil {
		t.Fatalf("выходной файл не существует: %v", readErr)
	}
	var raw json.RawMessage
	if jsonErr := json.Unmarshal(data, &raw); jsonErr != nil {
		t.Errorf("выходной файл содержит невалидный JSON: %v", jsonErr)
	}
}

// ── parseVLESSKey: IPv6 адрес сервера ────────────────────────────────────

// BUG-РИСК: URL.Hostname() для IPv6 возвращает адрес без скобок.
func TestParseVLESSKey_IPv6Address(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ipv6.key")
	url := "vless://uuid@[2001:db8::1]:443?sni=x.com&pbk=k&sid=s"
	os.WriteFile(path, []byte(url), 0644)

	p, err := parseVLESSKey(path)
	if err != nil {
		t.Fatalf("parseVLESSKey IPv6 вернул ошибку: %v", err)
	}
	if p.Address == "" {
		t.Error("Address не должен быть пустым для IPv6")
	}
	if p.Port != 443 {
		t.Errorf("Port = %d, want 443", p.Port)
	}
}

// ── parseVLESSKey: mux=false и mux=0 должны давать Mux=false ─────────────

func TestParseVLESSKey_MuxParam_False(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mux.key")
	os.WriteFile(path, []byte("vless://uuid@host.com:443?sni=x.com&pbk=k&sid=s&mux=false"), 0644)

	p, err := parseVLESSKey(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Mux {
		t.Error("Mux должен быть false при mux=false")
	}
}

func TestParseVLESSKey_MuxParam_Zero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mux.key")
	os.WriteFile(path, []byte("vless://uuid@host.com:443?sni=x.com&pbk=k&sid=s&mux=0"), 0644)

	p, err := parseVLESSKey(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Mux {
		t.Error("Mux должен быть false при mux=0")
	}
}

// ── buildVLESSOutbound: Flow + Mux несовместимы ───────────────────────────

// BUG-РИСК: XTLS Vision (flow=xtls-rprx-vision) несовместим с multiplexing.
// Если оба включены — sing-box упадёт при старте с ошибкой конфигурации.
func TestBuildVLESSOutbound_FlowAndMux_Incompatible(t *testing.T) {
	params := &VLESSParams{
		Address: "example.com", Port: 443,
		UUID: "u", SNI: "s", PublicKey: "k", ShortID: "i",
		Flow: "xtls-rprx-vision",
		Mux:  true, // пользователь указал оба параметра — должен быть сброшен mux
	}
	out := buildVLESSOutbound(params)
	if out.Flow != "" && out.Multiplex != nil && out.Multiplex.Enabled {
		t.Error("XTLS Vision (flow) несовместим с Multiplex — " +
			"buildVLESSOutbound должен отключать Mux при наличии Flow")
	}
}

// ── DetectRuleType: граничные случаи ──────────────────────────────────────

// BUG-РИСК: "192.168.1.1:8080" — не валидный IP (с портом), должен быть Domain.
func TestDetectRuleType_IPv4WithPort_IsNotIP(t *testing.T) {
	rt := DetectRuleType("192.168.1.1:8080")
	if rt == RuleTypeIP {
		t.Error("192.168.1.1:8080 (с портом) не является валидным IP — " +
			"должен быть Domain, а не IP")
	}
}

// BUG-РИСК: пустая строка не должна паниковать.
func TestDetectRuleType_EmptyString_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("DetectRuleType(\"\") вызвал panic: %v", r)
		}
	}()
	validTypes := map[RuleType]bool{
		RuleTypeProcess: true, RuleTypeDomain: true,
		RuleTypeIP: true, RuleTypeGeosite: true,
	}
	rt := DetectRuleType("")
	if !validTypes[rt] {
		t.Errorf("DetectRuleType(\"\") = %q — недопустимый тип", rt)
	}
}

// ── validateVLESSParams: whitespace-only поля ────────────────────────────

// BUG-РИСК: " " != "" — validate пропускает пробельные строки.
// Это вызовет cryptic ошибку в sing-box вместо понятного сообщения.
// Тест документирует баг и сразу поймает, когда добавишь TrimSpace.
func TestValidateVLESSParams_WhitespaceAddress_ShouldError(t *testing.T) {
	p := &VLESSParams{
		Address: "   ", Port: 443,
		UUID: "u", SNI: "s", PublicKey: "k", ShortID: "i",
	}
	err := validateVLESSParams(p)
	if err == nil {
		t.Error("validateVLESSParams должен отклонять пробельный Address — " +
			"добавь strings.TrimSpace в валидацию Address")
	}
}

func TestValidateVLESSParams_WhitespaceUUID_ShouldError(t *testing.T) {
	p := &VLESSParams{
		Address: "h.com", Port: 443,
		UUID: "\t", SNI: "s", PublicKey: "k", ShortID: "i",
	}
	err := validateVLESSParams(p)
	if err == nil {
		t.Error("validateVLESSParams должен отклонять пробельный UUID")
	}
}
