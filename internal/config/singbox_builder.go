package config

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"

	"proxyclient/internal/fileutil"
)

// ptrBool возвращает указатель на bool value
func ptrBool(b bool) *bool {
	return &b
}

// buildVLESSOutbound создаёт VLESS outbound с оптимальными параметрами.
// Автоматически определяет режим TLS по наличию PublicKey:
//   - PublicKey непустой → Reality (XTLS Reality + uTLS fingerprint)
//   - PublicKey пустой   → Plain TLS (стандартный TLS handshake)
//
// Мультиплексирование включается если:
//  1. params.Mux=true (URL содержит ?mux=1) — явно запрошено
//  2. Flow пустой — XTLS Vision несовместим с mux
func buildVLESSOutbound(params *VLESSParams) SBOutbound {
	tls := &SBTLS{
		Enabled:    true,
		ServerName: params.SNI,
		// random вместо chrome: случайный fingerprint из нескольких браузеров.
		// Усложняет детектирование трафика DPI системами, не влияет на скорость.
		UTLS: &SBUTLS{Enabled: true, Fingerprint: "random"},
	}
	// Reality включается только при наличии непустого PublicKey (pbk в URL).
	// Без PublicKey используется plain TLS — стандартный handshake без Reality.
	// BUG FIX: раньше Reality всегда включался с пустыми PublicKey/ShortID,
	// что приводило к крашу sing-box на серверах без Reality.
	if strings.TrimSpace(params.PublicKey) != "" {
		tls.Reality = &SBReality{
			Enabled:   true,
			PublicKey: params.PublicKey,
			ShortID:   params.ShortID,
		}
	}

	out := SBOutbound{
		Type:        "vless",
		Tag:         "proxy-out",
		Server:      params.Address,
		ServerPort:  params.Port,
		UUID:        params.UUID,
		Flow:        params.Flow,
		TLS:         tls,
		TCPFastOpen: ptrBool(true), // ускорение установки соединения (dial-field, valid v1.10+)
	}
	// Multiplex (mux): поддерживается с sing-box 1.6+.
	// Включается только при явном ?mux=1 в VLESS URL — безопасно для любой версии.
	// Без явного запроса — не включаем, чтобы не ломать старые версии sing-box.
	//
	// BUG FIX: XTLS Vision (flow=xtls-rprx-vision) несовместим с multiplexing.
	// Если пользователь задал оба параметра (?flow=xtls-rprx-vision&mux=1),
	// sing-box падает при старте с ошибкой конфигурации.
	// Flow имеет приоритет — отключаем mux при наличии flow.
	if params.Mux && params.Flow == "" {
		out.Multiplex = &SBMultiplex{
			Enabled:    true,
			Protocol:   "h2mux",
			MaxStreams: 8,
		}
	}
	return out
}

// buildDNSConfig создаёт DNS конфигурацию для sing-box на основе DNSConfig.
// Если dnsCfg == nil, использует значения по умолчанию.
func buildDNSConfig(dnsCfg *DNSConfig) SBDNS {
	if dnsCfg == nil {
		dnsCfg = DefaultDNSConfig()
	}

	// Парсим remote DNS URL — разбиваем на host, path и type.
	// BUG FIX: ранее parseDNSURL возвращал server="1.1.1.1/dns-query",
	// sing-box percent-кодировал слеш → "https://1.1.1.1%2Fdns-query/dns-query" → FATAL.
	remoteServer, remotePath, remoteType := parseDNSURL(dnsCfg.RemoteDNS)

	// Парсим direct DNS URL
	directServer, directPort, directType := parseDNSURLWithPort(dnsCfg.DirectDNS)

	return SBDNS{
		// ipv4_only: не возвращать AAAA записи — IPv6 недоступен на большинстве машин — подключение к IPv6-адресам
		// завершается "The requested address is not valid in its context".
		Strategy: "ipv4_only",
		Servers: []SBDNSServer{
			// Remote DNS идёт через прокси-туннель (detour: proxy-out).
			// Это предотвращает DNS leak: DNS-запросы не видны провайдеру.
			{Tag: "remote", Type: remoteType, Server: remoteServer, Path: remotePath, Detour: "proxy-out"},
			// Прямой DNS для локального трафика (http-in inbound) — без прокси.
			{Tag: "direct-dns", Type: directType, Server: directServer, ServerPort: directPort},
		},
		Rules: []SBDNSRule{
			{Inbound: []string{"http-in"}, Server: "direct-dns"},
		},
		Final: "remote",
	}
}

// parseDNSURL парсит DNS URL и возвращает (server, path, type).
// Поддерживает: https://host/path, tls://host, quic://host
//
// BUG FIX: для HTTPS URL разделяет хост и путь.
// "https://1.1.1.1/dns-query" → server="1.1.1.1", path="/dns-query", type="https"
// sing-box хранит host и path раздельно, иначе слеш в server percent-кодируется
// и получается невалидный URL "https://1.1.1.1%2Fdns-query/dns-query".
func parseDNSURL(url string) (server, path, typ string) {
	if strings.HasPrefix(url, "https://") {
		hostAndPath := strings.TrimPrefix(url, "https://")
		host, rest, hasPath := strings.Cut(hostAndPath, "/")
		if hasPath {
			return host, "/" + rest, "https"
		}
		return host, "", "https"
	}
	if strings.HasPrefix(url, "tls://") {
		return strings.TrimPrefix(url, "tls://"), "", "tls"
	}
	if strings.HasPrefix(url, "quic://") {
		return strings.TrimPrefix(url, "quic://"), "", "quic"
	}
	// Fallback: предполагаем https без пути
	return url, "", "https"
}

// parseDNSURLWithPort парсит DNS URL с портом и возвращает (server, port, type).
// Поддерживает: udp://host:port, tcp://host:port
func parseDNSURLWithPort(url string) (server string, port int, typ string) {
	if strings.HasPrefix(url, "udp://") {
		server = strings.TrimPrefix(url, "udp://")
		typ = "udp"
	} else if strings.HasPrefix(url, "tcp://") {
		server = strings.TrimPrefix(url, "tcp://")
		typ = "tcp"
	} else {
		// Fallback: plain server
		server = url
		typ = "udp"
	}

	// Разбираем host:port
	if host, portStr, err := splitHostPort(server); err == nil {
		if p, err := parsePortString(portStr); err == nil {
			return host, p, typ
		}
	}

	// Fallback: default port 53 for UDP
	return server, 53, typ
}

// splitHostPort разбивает "host:port" → (host, port).
// Корректно обрабатывает IPv6 адреса вида [::1]:9000.
func splitHostPort(addr string) (host, port string, err error) {
	i := len(addr) - 1
	for i >= 0 && addr[i] != ':' {
		i--
	}
	if i < 0 {
		return "", "", fmt.Errorf("отсутствует ':' в адресе %q", addr)
	}
	return addr[:i], addr[i+1:], nil
}

// parsePortString парсит строку в int (порт).
func parsePortString(s string) (int, error) {
	if len(s) == 0 {
		return 0, fmt.Errorf("пустая строка порта")
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("не цифра в порту: %q", c)
		}
		n = n*10 + int(c-'0')
	}
	if n <= 0 || n > 65535 {
		return 0, fmt.Errorf("порт %d вне диапазона 1–65535", n)
	}
	return n, nil
}

// buildSingBoxConfig собирает SingBoxConfig из уже подготовленных компонентов.
func buildSingBoxConfig(params *VLESSParams, outbound SBOutbound, tunExcludeAddr string, routingCfg *RoutingConfig) *SingBoxConfig {
	return &SingBoxConfig{
		Log: SBLog{Level: "warn"}, // info floods the log with every DNS query
		Experimental: SBExperimental{
			ClashAPI: SBClashAPI{
				ExternalController: ClashAPIAddr,
				Secret:             "",
			},
			// CacheFile: персистентный DNS кэш между перезапусками.
			// При рестарте sing-box не делает холодные DNS запросы —
			// отвечает из кэша пока резолвит в фоне. Ускоряет первые
			// соединения после перезапуска на 50-200мс.
			CacheFile: &SBCacheFile{
				Enabled: true,
				Path:    DataDir + "/dns_cache.db",
			},
		},
		DNS: buildDNSConfig(routingCfg.DNS),
		Inbounds: []SBInbound{
			{
				Type:       "http",
				Tag:        "http-in",
				Listen:     "127.0.0.1",
				ListenPort: ProxyPort,
				// sniff_override_destination намеренно не указан: поле удалено в sing-box 1.13.
				// Action "sniff" в route rules теперь всегда переопределяет destination.
			},
			buildTUN(tunExcludeAddr),
		},
		Outbounds: []SBOutbound{
			outbound,
			{Type: "direct", Tag: "direct"},
			// "block" outbound нужен для случая DefaultAction == ActionBlock,
			// а также для блокирующих правил route (action: "reject" — альтернатива,
			// но явный outbound обязателен когда final = "block").
			{Type: "block", Tag: "block"},
		},
		Route: buildRoute(routingCfg, tunExcludeAddr),
	}
}

// ipOrEmpty возвращает addr если это валидный IP, иначе "".
// Используется при построении CIDR-списков: hostname не может быть CIDR-адресом.
func ipOrEmpty(addr string) string {
	if net.ParseIP(addr) != nil {
		return addr
	}
	return ""
}

func GenerateSingBoxConfig(secretPath, outputPath string, routingCfg *RoutingConfig) error {
	params, err := parseVLESSKey(secretPath)
	if err != nil {
		return fmt.Errorf("ошибка парсинга VLESS ключа: %w", err)
	}
	if err := validateVLESSParams(params); err != nil {
		return fmt.Errorf("невалидные параметры: %w", err)
	}
	if routingCfg == nil {
		routingCfg = DefaultRoutingConfig()
	}

	outbound := buildVLESSOutbound(params)
	// Если адрес сервера — hostname (не IP), передаём пустую строку в buildSingBoxConfig.
	// buildTUN и buildRoute пропустят exclude-запись: hostname/32 — невалидный CIDR,
	// sing-box падает с "parse cidr: hostname/32: invalid CIDR address".
	tunExclude := ipOrEmpty(params.Address)
	cfg := buildSingBoxConfig(params, outbound, tunExclude, routingCfg)

	// BUG-2 FIX: вместо fatal error при отсутствии/повреждении geosite файла —
	// пропускаем его и убираем ссылающиеся правила. Один плохой файл не должен
	// блокировать генерацию конфига и применение всех остальных правил.
	skippedTags := map[string]bool{}
	validRuleSets := cfg.Route.RuleSet[:0]
	for _, rs := range cfg.Route.RuleSet {
		_, statErr := os.Stat(rs.Path)
		if os.IsNotExist(statErr) {
			// Файл не найден — пропускаем тег, продолжаем генерацию конфига.
			skippedTags[rs.Tag] = true
			continue
		}
		if statErr != nil || !IsSingBoxRuleSetFile(rs.Path) {
			// Файл повреждён или не является binary SRS — пропускаем.
			skippedTags[rs.Tag] = true
			continue
		}
		validRuleSets = append(validRuleSets, rs)
	}
	cfg.Route.RuleSet = validRuleSets
	// Убираем route rules ссылающиеся на пропущенные теги.
	if len(skippedTags) > 0 {
		validRouteRules := cfg.Route.Rules[:0]
		for _, rule := range cfg.Route.Rules {
			if len(rule.RuleSet) == 0 {
				validRouteRules = append(validRouteRules, rule)
				continue
			}

			filteredRuleSets := rule.RuleSet[:0]
			for _, tag := range rule.RuleSet {
				if !skippedTags[tag] {
					filteredRuleSets = append(filteredRuleSets, tag)
				}
			}
			if len(filteredRuleSets) > 0 {
				rule.RuleSet = filteredRuleSets
				validRouteRules = append(validRouteRules, rule)
			}
		}
		cfg.Route.Rules = validRouteRules
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("ошибка сериализации: %w", err)
	}

	// Атомарная запись через fileutil.WriteAtomic (MoveFileExW REPLACE_EXISTING).
	if err := fileutil.WriteAtomic(outputPath, data, 0644); err != nil {
		return fmt.Errorf("не удалось применить конфиг sing-box: %w", err)
	}
	return nil
}

func buildTUN(serverAddr string) SBInbound {
	return SBInbound{
		Type:          "tun",
		Tag:           "tun-in",
		InterfaceName: TunInterfaceName,
		Address:       []string{"172.20.0.1/30"},
		// MTU 1500 вместо 9000 (jumbo frames):
		// Jumbo frames требуют поддержки по всей цепочке (TUN → VPN сервер).
		// Большинство VPS и провайдеров не поддерживают jumbo frames на WAN.
		// Пакет 9000 байт будет фрагментирован или дропнут → переотправки → выше пинг.
		// 1500 = стандартный Ethernet MTU, гарантированно проходит везде.
		MTU:         1500,
		AutoRoute:   true,
		StrictRoute: true, // строгая маршрутизация: утечки трафика мимо TUN невозможны
		// mixed stack: system для TCP (нативный Windows стек, максимальная скорость)
		// + gVisor для UDP (корректное проксирование UDP через VLESS туннель).
		//
		// Почему не system:
		//   С system stack UDP (включая QUIC/HTTP3) обрабатывается Windows сокетами и
		//   может утекать мимо TUN даже при strict_route=true (Windows-специфичное поведение
		//   с binding сокетов к физическому интерфейсу). Итог: QUIC-трафик идёт напрямую
		//   с реального IP пользователя → Cloudflare видит российский IP → 403 Forbidden
		//   для chatgpt.com, api.openai.com (GPT, Codex), github.com (Copilot в VS Code).
		//
		// С mixed stack: gVisor перехватывает ALL UDP пакеты на уровне TUN-интерфейса,
		//   forwarding через VLESS → сервер → destination. QUIC (HTTP/3) к AI-сервисам
		//   корректно проксируется, VS Code Copilot / Codex / ChatGPT работают без 403.
		Stack: "mixed",
		// RouteExcludeAddress: IP прокси-сервера + loopback.
		// TUN подсеть (172.20.0.0/30) НЕ исключаем — иначе Windows DNS Client (svchost)
		// не получает ответы на DNS запросы к TUN-адресу → бесконечные ретраи.
		// Собственный трафик sing-box (включая его DNS) корректно выходит через
		// физический интерфейс благодаря auto_detect_interface: true.
		// Если serverAddr пустой (hostname, не IP) — exclude-запись не добавляем:
		// hostname/32 — невалидный CIDR, sing-box упадёт с parse cidr error.
		//
		// 127.0.0.0/8 и ::1/128 исключаются на уровне ОС (не только route-правила):
		// strict_route=true + WFP иногда перехватывает loopback трафик на Windows 11,
		// что ломает OAuth callback для Claude Code, Codex и других CLI-инструментов
		// (они стартуют HTTP-сервер на localhost:PORT для редиректа после авторизации).
		// RouteExcludeAddress гарантирует что loopback никогда не попадёт в TUN pipe.
		RouteExcludeAddress: func() []string {
			exclude := []string{"127.0.0.0/8", "::1/128"}
			if serverAddr != "" {
				exclude = append(exclude, serverAddr+"/32")
			}
			return exclude
		}(),
	}
}

// privateIPRanges — локальные и служебные диапазоны которые никогда не должны
// проксироваться. Аналог bypass-list в Clash Verge, Hiddify, Mihomo Party.
// Без этого: LAN-устройства недоступны, локальный DNS ломается.
var privateIPRanges = []string{
	"127.0.0.0/8",    // localhost
	"10.0.0.0/8",     // LAN class A
	"172.16.0.0/12",  // LAN class B
	"192.168.0.0/16", // LAN class C
	"169.254.0.0/16", // link-local
	"::1/128",        // IPv6 loopback
	"fc00::/7",       // IPv6 unique local
	"fe80::/10",      // IPv6 link-local
}

// telegramCIDRRanges — официальные Telegram CIDR из
// https://core.telegram.org/resources/cidr.txt. Telegram Desktop часто ходит
// напрямую по IP, поэтому domain/geosite и process_name правил недостаточно.
var telegramCIDRRanges = []string{
	"91.105.192.0/23",
	"91.108.4.0/22",
	"91.108.8.0/22",
	"91.108.12.0/22",
	"91.108.16.0/22",
	"91.108.20.0/22",
	"91.108.56.0/22",
	"149.154.160.0/20",
	"185.76.151.0/24",
	"2001:b28:f23c::/48",
	"2001:b28:f23d::/48",
	"2001:b28:f23f::/48",
	"2001:67c:4e8::/48",
	"2a0a:f280::/32",
}

func buildRoute(routingCfg *RoutingConfig, serverAddr string) SBRoute {
	if routingCfg == nil {
		routingCfg = DefaultRoutingConfig()
	}
	// BUG FIX #11: заменяем append(append(...)) на явный make+copy.
	// append([]string{}, privateIPRanges...) корректен сам по себе, но цепочка
	// append-ов хрупка — следующий append может создать алиасинг если ёмкость совпадёт.
	// Явный make с точным размером исключает любое алиасирование.
	// Если serverAddr пустой (hostname) — не добавляем "/32": невалидный CIDR.
	directCIDR := make([]string, len(privateIPRanges), len(privateIPRanges)+1)
	copy(directCIDR, privateIPRanges)
	if serverAddr != "" {
		directCIDR = append(directCIDR, serverAddr+"/32")
	}
	rules := []SBRouteRule{
		// Sniff: извлекаем domain из TLS SNI до применения routing rules.
		// Необходим для корректного domain/geosite matching через TUN.
		{Action: "sniff"},
		{Protocol: "dns", Action: "hijack-dns"},
		// Локальные адреса и IP прокси-сервера всегда напрямую — ПЕРВЫМ приоритетом.
		// serverAddr исключён явно: TUN с auto_route перехватывает весь трафик включая
		// собственные соединения sing-box к серверу → routing loop без этого правила.
		// ПОРЯДОК ВАЖЕН: это правило должно быть раньше любых proxy-out правил,
		// чтобы приватные IP (192.168.x.x, 10.x.x.x, 127.x.x.x) не попали в прокси.
		{IPCIDR: directCIDR, Outbound: "direct"},
		// Telegram часто обращается к DC по IP, а не по домену. Это правило должно
		// быть до blanket IPv6 reject, иначе IPv6 Telegram DC будут отброшены.
		{IPCIDR: telegramCIDRRanges, Outbound: "proxy-out"},
		// IPv6 глобальный unicast: DNS strategy=ipv4_only предотвращает новые AAAA lookup,
		// но приложения с hardcoded IPv6 адресами (Telegram DC: 2001:b28:f23d::/48 и др.)
		// всё равно пытаются подключиться напрямую к IPv6 адресам.
		// На машинах без IPv6 это приводит к flood ошибок в логах:
		//   "The requested address is not valid in its context" каждые несколько секунд.
		// Отклоняем все IPv6 глобальные unicast соединения принудительно:
		//   - Приложения (Telegram, Firefox, Chrome) переключатся на IPv4
		//   - IPv4 трафик нормально проходит через правила ниже
		// Исключения уже обработаны правилом directCIDR выше:
		//   ::1/128 (loopback), fc00::/7 (ULA), fe80::/10 (link-local) → direct
		// ::/0 матчит всё оставшееся IPv6 (включая 2000::/3 global unicast).
		{IPCIDR: []string{"::/0"}, Action: "reject"},
	}

	if routingCfg.BypassEnabled {
		return SBRoute{
			Rules:                 rules,
			Final:                 "proxy-out",
			AutoDetectInterface:   true,
			DefaultDomainResolver: "direct-dns",
		}
	}

	rules = append(rules,
		// QUIC (UDP/443) для AI-сервисов: явно пропускаем через прокси ДО blanket reject.
		// VS Code Codex, GitHub Copilot, ChatGPT используют HTTP/3 (QUIC) для API-вызовов.
		// С mixed stack + gVisor gVisor перехватывает UDP и форвардит через VLESS туннель.
		// DomainSuffix матчится по SNI (sniff выше извлекает его из QUIC Initial packet).
		// Приватные IP уже обработаны выше — сюда доходит только публичный трафик AI-сервисов.
		SBRouteRule{
			Network: "udp",
			Port:    []uint16{443},
			DomainSuffix: []string{
				"openai.com",         // ChatGPT, Codex API, Whisper
				"chatgpt.com",        // ChatGPT web
				"oaistatic.com",      // OpenAI static assets
				"oaiusercontent.com", // OpenAI user content
				"github.com",         // GitHub Copilot (VS Code extension)
				"githubcopilot.com",  // GitHub Copilot API
				"copilot.github.com", // GitHub Copilot
				"anthropic.com",      // Claude API
				"claude.ai",          // Claude web
			},
			Outbound: "proxy-out",
		},
		// Blanket-reject остального QUIC (UDP/443): браузеры переключаются на TCP/443.
		// Без этого браузер кэширует "Alt-Svc: h3" и пытается использовать QUIC для всех сайтов,
		// что с некоторыми серверами даёт некорректную маршрутизацию.
		// AI-сервисы выше уже обработаны — сюда попадают только прочие домены.
		SBRouteRule{Network: "udp", Port: []uint16{443}, Action: "reject"},
	)

	var proxyProcs, directProcs, blockProcs []string
	var proxyDom, directDom, blockDom []string
	var proxySuf, directSuf, blockSuf []string
	var proxyIP, directIP, blockIP []string
	var proxyGeosite, directGeosite, blockGeosite []string

	for _, rule := range routingCfg.Rules {
		val := rule.Value
		switch rule.Type {
		case RuleTypeProcess:
			switch rule.Action {
			case ActionProxy:
				proxyProcs = append(proxyProcs, val)
			case ActionDirect:
				directProcs = append(directProcs, val)
			case ActionBlock:
				blockProcs = append(blockProcs, val)
			}
		case RuleTypeIP:
			switch rule.Action {
			case ActionProxy:
				proxyIP = append(proxyIP, val)
			case ActionDirect:
				directIP = append(directIP, val)
			case ActionBlock:
				blockIP = append(blockIP, val)
			}
		case RuleTypeDomain:
			if strings.HasPrefix(val, ".") {
				sfx := strings.TrimPrefix(val, ".")
				switch rule.Action {
				case ActionProxy:
					proxySuf = append(proxySuf, sfx)
				case ActionDirect:
					directSuf = append(directSuf, sfx)
				case ActionBlock:
					blockSuf = append(blockSuf, sfx)
				}
			} else {
				// BUG FIX: ранее plain-домен (без точки) попадал ТОЛЬКО в Domain["twitch.tv"],
				// что в sing-box матчит ТОЛЬКО точное совпадение.
				// Субдомены ("gql.twitch.tv", "video-edge-47127a.twitch.tv" и др.) —
				// не матчились → продолжали идти через прокси даже при правиле direct.
				// Исправление: добавляем также в DomainSuffix — покрывает и сам домен,
				// и все поддомены одновременно.
				switch rule.Action {
				case ActionProxy:
					proxyDom = append(proxyDom, val)
					proxySuf = append(proxySuf, val)
				case ActionDirect:
					directDom = append(directDom, val)
					directSuf = append(directSuf, val)
				case ActionBlock:
					blockDom = append(blockDom, val)
					blockSuf = append(blockSuf, val)
				}
			}
		case RuleTypeGeosite:
			tag := strings.TrimPrefix(val, "geosite:")
			switch rule.Action {
			case ActionProxy:
				proxyGeosite = append(proxyGeosite, "geosite-"+tag)
			case ActionDirect:
				directGeosite = append(directGeosite, "geosite-"+tag)
			case ActionBlock:
				blockGeosite = append(blockGeosite, "geosite-"+tag)
			}
		}
	}

	var ruleSets []SBRuleSet
	// BUG FIX: append(proxyGeosite, directGeosite...) мутирует proxyGeosite
	// если у слайса есть свободная ёмкость (cap > len) — данные для addRule портятся.
	// Собираем allTags в отдельный слайс с явным cap чтобы избежать алиасинга.
	allTags := make([]string, 0, len(proxyGeosite)+len(directGeosite)+len(blockGeosite))
	allTags = append(allTags, proxyGeosite...)
	allTags = append(allTags, directGeosite...)
	allTags = append(allTags, blockGeosite...)
	seen := map[string]bool{}
	for _, tag := range allTags {
		if !seen[tag] {
			seen[tag] = true
			path := DataDir + "/" + tag + ".bin"
			ruleSets = append(ruleSets, SBRuleSet{
				Type:   "local",
				Tag:    tag,
				Format: "binary",
				Path:   path,
			})
		}
	}

	// addDomainRule добавляет одно правило только по домен/IP/суффикс критериям.
	addDomainRule := func(out, action string, doms, sufs, ips []string) {
		if len(doms)+len(sufs)+len(ips) == 0 {
			return
		}
		r := SBRouteRule{}
		if out != "" {
			r.Outbound = out
		} else {
			r.Action = action
		}
		if len(doms) > 0 {
			r.Domain = doms
		}
		if len(sufs) > 0 {
			r.DomainSuffix = sufs
		}
		if len(ips) > 0 {
			r.IPCIDR = ips
		}
		rules = append(rules, r)
	}

	// ── Умная сортировка правил (Smart Rule Priority) ──────────────────────────
	//
	// Порядок важен: sing-box применяет первое совпавшее правило.
	//
	// Проблема старого порядка (все direct → все proxy):
	//   chrome.exe→proxy  +  google.com→direct
	//   Генерировало: [direct:proc=chrome? нет] → [direct:dom=google] → [proxy:proc=chrome]
	//   chrome→google.com совпадал с "direct:dom=google" раньше "proxy:proc=chrome" → правильно
	//   НО: chrome.exe→proxy + youtube.com→proxy (default=direct)
	//   Генерировало: [direct: ничего] → [proxy:proc=chrome] → [proxy:dom=youtube]
	//   firefox→youtube: нет proxy:proc → нет proxy:dom (оба в одном правиле)... зависит от порядка
	//
	// Правильный порядок по специфичности:
	//   1. BLOCK всё (процессы, домены, geosite) — безопасность прежде всего
	//   2. Конкретные domain/IP правила (direct И proxy) — переопределяют process-правила
	//      Пример: google.com→direct перекрывает chrome.exe→proxy для google.com ✓
	//              youtube.com→proxy перекрывает default=direct для всех процессов ✓
	//   3. Process правила (direct и proxy) — широкие правила по источнику
	//   4. Geosite правила — самые широкие паттерны
	//
	// Итог: domain/IP правила всегда применяются раньше process-правил,
	// что соответствует интуитивному ожиданию пользователя.

	// Шаг 1: BLOCK — процессы
	if len(blockProcs) > 0 {
		rules = append(rules, SBRouteRule{ProcessName: blockProcs, Action: "reject"})
	}
	// BLOCK — домены/IP
	addDomainRule("", "reject", blockDom, blockSuf, blockIP)
	// BLOCK — geosite
	if len(blockGeosite) > 0 {
		rules = append(rules, SBRouteRule{RuleSet: blockGeosite, Action: "reject"})
	}

	// Шаг 2: Конкретные domain/IP правила (оба действия — direct и proxy)
	// direct domain/IP: например google.com→direct при default=proxy
	addDomainRule("direct", "", directDom, directSuf, directIP)
	// proxy domain/IP: например youtube.com→proxy при default=direct
	addDomainRule("proxy-out", "", proxyDom, proxySuf, proxyIP)

	// Шаг 3: Process правила
	if len(directProcs) > 0 {
		rules = append(rules, SBRouteRule{ProcessName: directProcs, Outbound: "direct"})
	}
	if len(proxyProcs) > 0 {
		rules = append(rules, SBRouteRule{ProcessName: proxyProcs, Outbound: "proxy-out"})
	}

	// Шаг 4: Geosite правила (самые широкие)
	if len(directGeosite) > 0 {
		rules = append(rules, SBRouteRule{RuleSet: directGeosite, Outbound: "direct"})
	}
	if len(proxyGeosite) > 0 {
		rules = append(rules, SBRouteRule{RuleSet: proxyGeosite, Outbound: "proxy-out"})
	}

	final := "proxy-out"
	if routingCfg.DefaultAction == ActionDirect {
		final = "direct"
	} else if routingCfg.DefaultAction == ActionBlock {
		final = "block"
	}

	// FindProcess: включаем только если есть process_name правила.
	// Детектирование процесса добавляет syscall на каждое новое соединение —
	// включаем только когда реально нужно, чтобы не добавлять накладные расходы зря.
	hasProcessRules := len(proxyProcs)+len(directProcs)+len(blockProcs) > 0

	return SBRoute{
		Rules:   rules,
		RuleSet: ruleSets,
		Final:   final,
		// AutoDetectInterface: true — обязателен при auto_route=true.
		// Без этого sing-box не знает какой физический интерфейс использовать для
		// direct-трафика и отправляет его обратно в TUN → routing loop на 172.20.0.2.
		// Производительность: syscall при новом соединении незначителен по сравнению с петлёй.
		AutoDetectInterface:   true,
		DefaultDomainResolver: "direct-dns",
		FindProcess:           hasProcessRules,
	}
}
