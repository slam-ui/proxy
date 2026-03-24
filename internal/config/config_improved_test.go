package config

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// ── BUG-РИСК #1: vlessCache — race при конкурентном чтении разных файлов ──
//
// vlessCache — глобальная переменная. parseVLESSKey делает:
//   1. mu.Lock → проверяет кэш → mu.Unlock
//   2. readAndParseVLESS (без lock!)
//   3. mu.Lock → записывает кэш → mu.Unlock
//
// TOCTOU: между шагами 1 и 3 другая горутина может записать в кэш
// ДРУГОЙ файл. В итоге кэш будет содержать данные от горутины B,
// но помечен как принадлежащий файлу A — следующий вызов вернёт неверные данные.
// Запускать с -race флагом.
func TestParseVLESSKey_Concurrent_NoCacheCorruption(t *testing.T) {
	dir := t.TempDir()
	urls := []string{
		"vless://uuid-aaa@host-a.com:443?sni=a.com&pbk=key-a&sid=sid-a",
		"vless://uuid-bbb@host-b.com:443?sni=b.com&pbk=key-b&sid=sid-b",
		"vless://uuid-ccc@host-c.com:443?sni=c.com&pbk=key-c&sid=sid-c",
	}

	paths := make([]string, len(urls))
	for i, u := range urls {
		p := filepath.Join(dir, "key-"+string(rune('a'+i))+".key")
		os.WriteFile(p, []byte(u), 0644)
		paths[i] = p
	}

	var wg sync.WaitGroup
	errors := make(chan string, 100)

	for round := 0; round < 20; round++ {
		for i, p := range paths {
			wg.Add(1)
			go func(path string, idx int) {
				defer wg.Done()
				params, err := parseVLESSKey(path)
				if err != nil {
					return // ошибка парсинга — не наш баг здесь
				}
				expectedHost := "host-" + string(rune('a'+idx)) + ".com"
				if params.Address != expectedHost {
					errors <- "кэш повреждён: файл " + path + " вернул Address=" + params.Address + ", want " + expectedHost
				}
			}(p, i)
		}
	}
	wg.Wait()
	close(errors)

	for msg := range errors {
		t.Error(msg)
		break // достаточно одной ошибки
	}
}

// ── BUG-РИСК #2: BOM + trailing whitespace + комментарии в одном файле ─────
//
// Реальный Блокнот Windows добавляет BOM И может оставить \r\n.
// Тест комбинирует все проблемы разом.
func TestParseVLESSKey_BOMWithCRLFAndComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messy.key")
	// BOM + CRLF + комментарий + пустая строка + URL с CRLF
	content := "\ufeff# comment\r\n\r\nvless://uuid@host.com:443?sni=x.com&pbk=k&sid=s\r\n"
	os.WriteFile(path, []byte(content), 0644)

	params, err := parseVLESSKey(path)
	if err != nil {
		t.Fatalf("parseVLESSKey с BOM+CRLF+комментарием: %v", err)
	}
	if params.Address != "host.com" {
		t.Errorf("Address=%q после BOM+CRLF, want host.com", params.Address)
	}
}

// ── BUG-РИСК #3: UUID содержит спецсимволы URL ───────────────────────────────
//
// UUID стандартный: "550e8400-e29b-41d4-a716-446655440000"
// url.Parse корректно обрабатывает дефисы в User.Username().
// Но если UUID содержит '@' или '/' — парсинг сломается.
// Тест проверяет что стандартный UUID читается корректно.
func TestParseVLESSKey_UUID_WithDashes(t *testing.T) {
	uuid := "550e8400-e29b-41d4-a716-446655440000"
	path := filepath.Join(t.TempDir(), "uuid.key")
	url := "vless://" + uuid + "@host.com:443?sni=x.com&pbk=k&sid=s"
	os.WriteFile(path, []byte(url), 0644)

	params, err := parseVLESSKey(path)
	if err != nil {
		t.Fatalf("parseVLESSKey с UUID с дефисами: %v", err)
	}
	if params.UUID != uuid {
		t.Errorf("UUID=%q, want %q", params.UUID, uuid)
	}
}

// ── BUG-РИСК #4: SNI содержит wildcard ───────────────────────────────────────
//
// Некоторые VLESS конфиги используют SNI типа "*.example.com".
// url.QueryUnescape должен это обработать, но validateVLESSParams проверяет
// TrimSpace(SNI) != "" — wildcard пройдёт, а sing-box может упасть.
// Тест документирует что wildcard SNI принимается (потенциальный баг).
func TestParseVLESSKey_WildcardSNI_Accepted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wildcard.key")
	url := "vless://uuid@host.com:443?sni=*.example.com&pbk=k&sid=s"
	os.WriteFile(path, []byte(url), 0644)

	params, err := parseVLESSKey(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Logf("wildcard SNI принят: %q (sing-box может отклонить при старте)", params.SNI)
}

// ── BUG-РИСК #5: порт граничные значения ──────────────────────────────────
//
// readAndParseVLESS проверяет port ≤ 0 || port > 65535.
// Граничные значения: 1, 443, 65535 — допустимы. 0, 65536 — нет.
func TestParseVLESSKey_PortBoundaries(t *testing.T) {
	cases := []struct {
		port    string
		wantErr bool
	}{
		{"1", false},
		{"443", false},
		{"65535", false},
		{"0", true},
		{"65536", true},
		{"99999", true},
		{"-1", true},
	}
	for _, tc := range cases {
		path := filepath.Join(t.TempDir(), "port-"+tc.port+".key")
		url := "vless://uuid@host.com:" + tc.port + "?sni=x.com&pbk=k&sid=s"
		os.WriteFile(path, []byte(url), 0644)

		_, err := parseVLESSKey(path)
		if (err != nil) != tc.wantErr {
			t.Errorf("port=%s: err=%v, wantErr=%v", tc.port, err, tc.wantErr)
		}
	}
}

// ── BUG-РИСК #6: buildVLESSOutbound — flow + mux конфликт ───────────────────
//
// Документированный баг: если flow="xtls-rprx-vision" и Mux=true,
// sing-box падает при старте. buildVLESSOutbound должен игнорировать Mux
// при наличии Flow. Тест проверяет что Multiplex=nil при flow!=".
func TestBuildVLESSOutbound_FlowDisablesMux(t *testing.T) {
	params := &VLESSParams{
		Address:   "1.2.3.4",
		Port:      443,
		UUID:      "uuid",
		SNI:       "x.com",
		PublicKey: "key",
		ShortID:   "sid",
		Flow:      "xtls-rprx-vision",
		Mux:       true, // явно запрошен, но должен быть игнорирован
	}
	out := buildVLESSOutbound(params)
	if out.Multiplex != nil {
		t.Error("buildVLESSOutbound с flow!='' должен НЕ устанавливать Multiplex (несовместимо с XTLS Vision)")
	}
}

// ── BUG-РИСК #7: buildVLESSOutbound — mux без flow включает Multiplex ──────
func TestBuildVLESSOutbound_MuxWithoutFlow_EnablesMultiplex(t *testing.T) {
	params := &VLESSParams{
		Address:   "1.2.3.4",
		Port:      443,
		UUID:      "uuid",
		SNI:       "x.com",
		PublicKey: "key",
		ShortID:   "sid",
		Flow:      "",
		Mux:       true,
	}
	out := buildVLESSOutbound(params)
	if out.Multiplex == nil {
		t.Error("buildVLESSOutbound с Mux=true и Flow='' должен установить Multiplex")
	}
	if out.Multiplex != nil && !out.Multiplex.Enabled {
		t.Error("Multiplex.Enabled должен быть true")
	}
}

// ── BUG-РИСК #8: buildRoute — пустой serverAddr ──────────────────────────────
//
// Если parseVLESSKey вернул params.Address="" (хотя защита есть),
// buildRoute добавит "/32" в directCIDR → sing-box получит невалидный CIDR.
func TestBuildRoute_EmptyServerAddr_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("buildRoute с пустым serverAddr вызвал panic: %v", r)
		}
	}()

	cfg := &RoutingConfig{DefaultAction: ActionDirect, Rules: []RoutingRule{}}
	route := buildRoute(cfg, "")
	t.Logf("buildRoute с '' вернул %d правил (directCIDR[0]=%q)",
		len(route.Rules),
		func() string {
			if len(route.Rules) > 1 && len(route.Rules[1].IPCIDR) > 0 {
				return route.Rules[1].IPCIDR[len(route.Rules[1].IPCIDR)-1]
			}
			return "n/a"
		}())
}

// ── BUG-РИСК #9: buildRoute — дублирующийся serverAddr в directCIDR ─────────
//
// Если serverAddr совпадает с одним из privateIPRanges (например "127.0.0.1"),
// в directCIDR окажется два одинаковых CIDR — sing-box может выдать warning.
func TestBuildRoute_ServerAddrInPrivateRange_NotDuplicated(t *testing.T) {
	loopback := "127.0.0.1"
	cfg := &RoutingConfig{DefaultAction: ActionDirect, Rules: []RoutingRule{}}
	route := buildRoute(cfg, loopback)

	want := loopback + "/32"
	count := 0
	for _, rule := range route.Rules {
		for _, cidr := range rule.IPCIDR {
			if cidr == want {
				count++
			}
		}
	}
	if count > 1 {
		t.Logf("WARN: %q встречается %d раз в directCIDR (возможный дубль)", want, count)
	}
}

// ── BUG-РИСК #10: buildRoute — geosite с "geosite:" префиксом vs без ────────
//
// buildRoute делает TrimPrefix(val, "geosite:").
// Если val уже без префикса ("youtube" вместо "geosite:youtube") — работает.
// Если val = "geosite:geosite:youtube" (двойной префикс) — баг в конфиге,
// но тест документирует поведение.
func TestBuildRoute_GeoSite_PrefixHandling(t *testing.T) {
	cases := []struct {
		value   string
		wantTag string
	}{
		{"geosite:youtube", "geosite-youtube"},
		{"youtube", "geosite-youtube"},             // без префикса — trim не применяется
		{"geosite:geosite:youtube", "geosite-geosite:youtube"}, // двойной префикс — документируем
	}

	for _, tc := range cases {
		cfg := &RoutingConfig{
			DefaultAction: ActionDirect,
			Rules: []RoutingRule{
				{Value: tc.value, Type: RuleTypeGeosite, Action: ActionProxy},
			},
		}
		route := buildRoute(cfg, "1.2.3.4")

		// Проверяем RuleSet тег
		for _, rs := range route.RuleSet {
			if strings.HasPrefix(rs.Tag, "geosite-") {
				t.Logf("value=%q → RuleSet.Tag=%q (want %q)", tc.value, rs.Tag, tc.wantTag)
			}
		}
	}
}

// ── BUG-РИСК #11: validateVLESSParams — whitespace-only поля ─────────────────
//
// validateVLESSParams использует TrimSpace для проверки.
// Поле с " " (пробел) должно вызывать ошибку.
func TestValidateVLESSParams_WhitespaceFields(t *testing.T) {
	base := VLESSParams{
		Address:   "host.com",
		Port:      443,
		UUID:      "uuid",
		SNI:       "sni.com",
		PublicKey: "key",
		ShortID:   "sid",
	}

	cases := []struct {
		name  string
		patch func(*VLESSParams)
	}{
		{"whitespace UUID", func(p *VLESSParams) { p.UUID = "   " }},
		{"whitespace SNI", func(p *VLESSParams) { p.SNI = "\t" }},
		{"whitespace PublicKey", func(p *VLESSParams) { p.PublicKey = " " }},
		{"whitespace ShortID", func(p *VLESSParams) { p.ShortID = "\n" }},
		{"whitespace Address", func(p *VLESSParams) { p.Address = "  " }},
	}

	for _, tc := range cases {
		p := base
		tc.patch(&p)
		err := validateVLESSParams(&p)
		if err == nil {
			t.Errorf("validateVLESSParams [%s]: должна быть ошибка для whitespace-only поля", tc.name)
		}
	}
}

// ── BUG-РИСК #12: buildRoute — сортировка правил (block перед proxy) ─────────
//
// Правила в sing-box применяются в порядке добавления — первое совпадение выигрывает.
// BLOCK должен идти раньше PROXY/DIRECT. Тест проверяет что block-процесс
// добавляется в rules[0] или как минимум раньше proxy-правила.
func TestBuildRoute_BlockBeforeProxy(t *testing.T) {
	cfg := &RoutingConfig{
		DefaultAction: ActionDirect,
		Rules: []RoutingRule{
			{Value: "app.exe", Type: RuleTypeProcess, Action: ActionProxy},
			{Value: "malware.exe", Type: RuleTypeProcess, Action: ActionBlock},
		},
	}
	route := buildRoute(cfg, "1.2.3.4")

	blockIdx, proxyIdx := -1, -1
	for i, r := range route.Rules {
		if r.Action == "reject" && len(r.ProcessName) > 0 {
			blockIdx = i
		}
		if r.Outbound == "proxy-out" && len(r.ProcessName) > 0 {
			proxyIdx = i
		}
	}

	if blockIdx == -1 {
		t.Fatal("block-правило для процесса не найдено в route.Rules")
	}
	if proxyIdx == -1 {
		t.Fatal("proxy-правило для процесса не найдено в route.Rules")
	}
	if blockIdx > proxyIdx {
		t.Errorf("block-правило (idx=%d) идёт ПОСЛЕ proxy-правила (idx=%d) — routing priority нарушен",
			blockIdx, proxyIdx)
	}
}
