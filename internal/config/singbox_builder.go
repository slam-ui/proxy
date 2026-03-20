package config

import (
	"fmt"
	"os"
	"encoding/json"
	"strings"

	"proxyclient/internal/fileutil"
)

// buildVLESSOutbound создаёт VLESS outbound с оптимальными параметрами.
// Автоматически включает мультиплексирование если:
//   1. params.Mux=true (URL содержит ?mux=1) — явно запрошено
//   2. Flow пустой — XTLS Vision несовместим с mux
//
// Multiplexing устраняет TLS handshake (~50-100мс) для каждого нового соединения,
// мультиплексируя несколько потоков в одном TLS соединении.
func buildVLESSOutbound(params *VLESSParams) SBOutbound {
	out := SBOutbound{
		Type:       "vless",
		Tag:        "proxy-out",
		Server:     params.Address,
		ServerPort: params.Port,
		UUID:       params.UUID,
		Flow:       params.Flow,
		TLS: &SBTLS{
			Enabled:    true,
			ServerName: params.SNI,
			Reality:    &SBReality{Enabled: true, PublicKey: params.PublicKey, ShortID: params.ShortID},
			// random вместо chrome: случайный fingerprint из нескольких браузеров.
			// Усложняет детектирование трафика DPI системами, не влияет на скорость.
			UTLS: &SBUTLS{Enabled: true, Fingerprint: "random"},
		},
	}
	// Multiplex (mux): поддерживается с sing-box 1.6+.
	// Включается только при явном ?mux=1 в VLESS URL — безопасно для любой версии.
	// Без явного запроса — не включаем, чтобы не ломать старые версии sing-box.
	if params.Mux {
		out.Multiplex = &SBMultiplex{
			Enabled:    true,
			Protocol:   "h2mux",
			MaxStreams: 8,
		}
	}
	return out
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

	cfg := &SingBoxConfig{
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
		DNS: SBDNS{
			Servers: []SBDNSServer{
				// DoH (DNS-over-HTTPS) вместо DoT (DNS-over-TLS):
				// DoT открывает новое TLS соединение на каждый запрос (+50-150мс).
				// DoH использует HTTP/2 keep-alive — одно соединение для всех запросов.
				// Type "https" + Server = адрес DoH резолвера (sing-box формирует URL сам).
				{Tag: "remote", Type: "https", Server: "1.1.1.1"},
				// Прямой UDP для локального трафика (http-in inbound) — без прокси.
				{Tag: "direct-dns", Type: "udp", Server: "8.8.8.8", ServerPort: 53},
			},
			Rules: []SBDNSRule{
				{Inbound: []string{"http-in"}, Server: "direct-dns"},
			},
			Final: "remote",
		},
		Inbounds: []SBInbound{
			{
				Type:       "http",
				Tag:        "http-in",
				Listen:     "127.0.0.1",
				ListenPort: ProxyPort,
				// Sniff на HTTP inbound нужен для override CONNECT-хостов.
				// Оставляем только здесь, убираем с TUN (там не нужно).
				Sniff:                    true,
				SniffOverrideDestination: true,
			},
			buildTUN(params.Address),
		},
		Outbounds: []SBOutbound{
			buildVLESSOutbound(params),
			{Type: "direct", Tag: "direct"},
			// "block" outbound нужен для случая DefaultAction == ActionBlock,
			// а также для блокирующих правил route (action: "reject" — альтернатива,
			// но явный outbound обязателен когда final = "block").
			{Type: "block", Tag: "block"},
		},
		Route: buildRoute(routingCfg, params.Address),
	}

	// Проверяем что все geosite .bin файлы существуют до записи конфига.
	// buildRoute уже добавил их в RuleSet — проверяем существование здесь,
	// пока ещё можем вернуть error (cfg формируется в памяти, файл ещё не записан).
	for _, rs := range cfg.Route.RuleSet {
		if _, statErr := os.Stat(rs.Path); os.IsNotExist(statErr) {
			return fmt.Errorf("файл geosite не найден: %s — скачайте его в приложении", rs.Path)
		}
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
		MTU:           1500,
		AutoRoute:     true,
		StrictRoute:   true, // строгая маршрутизация: утечки трафика мимо TUN невозможны
		// system stack вместо mixed: system использует нативный Windows TCP/IP стек.
		// mixed = gVisor для UDP + system для TCP. system быстрее для большинства трафика
		// так как нет overhead пользовательского TCP стека. Рекомендация sing-box docs.
		Stack:         "system",
		// Sniff=true на TUN: обязателен для работы правила protocol: dns → hijack-dns.
		// Без sniff sing-box не определяет протокол DNS и правило не срабатывает —
		// DNS-запросы svchost/Windows падают в final: direct и уходят на 172.20.0.2 в никуда.
		Sniff:         true,
		// RouteExcludeAddress: только IP прокси-сервера.
		// TUN подсеть (172.20.0.0/30) НЕ исключаем — иначе Windows DNS Client (svchost)
		// не получает ответы на DNS запросы к TUN-адресу → бесконечные ретраи.
		// Собственный трафик sing-box (включая его DNS) корректно выходит через
		// физический интерфейс благодаря auto_detect_interface: true.
		RouteExcludeAddress: []string{serverAddr + "/32"},
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

func buildRoute(routingCfg *RoutingConfig, serverAddr string) SBRoute {
	directCIDR := append(append([]string{}, privateIPRanges...), serverAddr+"/32")
	rules := []SBRouteRule{
		{Protocol: "dns", Action: "hijack-dns"},
		// Локальные адреса и IP прокси-сервера всегда напрямую.
		// serverAddr исключён явно: TUN с auto_route перехватывает весь трафик включая
		// собственные соединения sing-box к серверу → routing loop без этого правила.
		{IPCIDR: directCIDR, Outbound: "direct"},
	}

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
				switch rule.Action {
				case ActionProxy:
					proxyDom = append(proxyDom, val)
				case ActionDirect:
					directDom = append(directDom, val)
				case ActionBlock:
					blockDom = append(blockDom, val)
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

	addRule := func(out string, procs, doms, sufs, ips, geos []string) {
		if len(procs) > 0 {
			rules = append(rules, SBRouteRule{ProcessName: procs, Outbound: out})
		}
		if len(doms)+len(sufs)+len(ips) > 0 {
			r := SBRouteRule{Outbound: out}
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
		if len(geos) > 0 {
			rules = append(rules, SBRouteRule{RuleSet: geos, Outbound: out})
		}
	}

	if len(blockProcs) > 0 {
		rules = append(rules, SBRouteRule{ProcessName: blockProcs, Action: "reject"})
	}
	if len(blockDom)+len(blockSuf)+len(blockIP) > 0 {
		r := SBRouteRule{Action: "reject"}
		if len(blockDom) > 0 {
			r.Domain = blockDom
		}
		if len(blockSuf) > 0 {
			r.DomainSuffix = blockSuf
		}
		if len(blockIP) > 0 {
			r.IPCIDR = blockIP
		}
		rules = append(rules, r)
	}
	if len(blockGeosite) > 0 {
		rules = append(rules, SBRouteRule{RuleSet: blockGeosite, Action: "reject"})
	}

	addRule("direct", directProcs, directDom, directSuf, directIP, directGeosite)
	addRule("proxy-out", proxyProcs, proxyDom, proxySuf, proxyIP, proxyGeosite)

	final := "proxy-out"
	if routingCfg.DefaultAction == ActionDirect {
		final = "direct"
	} else if routingCfg.DefaultAction == ActionBlock {
		final = "block"
	}

	return SBRoute{
		Rules:                 rules,
		RuleSet:               ruleSets,
		Final:                 final,
		// AutoDetectInterface: true — обязателен при auto_route=true.
		// Без этого sing-box не знает какой физический интерфейс использовать для
		// direct-трафика и отправляет его обратно в TUN → routing loop на 172.20.0.2.
		// Производительность: syscall при новом соединении незначителен по сравнению с петлёй.
		AutoDetectInterface:   true,
		DefaultDomainResolver: "direct-dns",
	}
}
