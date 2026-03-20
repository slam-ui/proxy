package config

import (
	"fmt"
	"os"
	"encoding/json"
	"strings"

	"proxyclient/internal/fileutil"
)

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
		},
		DNS: SBDNS{
			Servers: []SBDNSServer{
				{Tag: "remote", Type: "tls", Server: "8.8.8.8", ServerPort: 853},
				{Tag: "direct-dns", Type: "udp", Server: "1.1.1.1", ServerPort: 53},
			},
			Rules: []SBDNSRule{
				{Inbound: []string{"http-in"}, Server: "direct-dns"},
			},
			Final: "remote",
		},
		Inbounds: []SBInbound{
			{
				Type:                     "http",
				Tag:                      "http-in",
				Listen:                   "127.0.0.1",
				ListenPort:               ProxyPort,
				Sniff:                    true,
				SniffOverrideDestination: true,
			},
			buildTUN(),
		},
		Outbounds: []SBOutbound{
			{
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
					UTLS:       &SBUTLS{Enabled: true, Fingerprint: "chrome"},
				},
			},
			{Type: "direct", Tag: "direct"},
			// "block" outbound нужен для случая DefaultAction == ActionBlock,
			// а также для блокирующих правил route (action: "reject" — альтернатива,
			// но явный outbound обязателен когда final = "block").
			{Type: "block", Tag: "block"},
		},
		Route: buildRoute(routingCfg),
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

func buildTUN() SBInbound {
	return SBInbound{
		Type:                     "tun",
		Tag:                      "tun-in",
		InterfaceName:            TunInterfaceName,
		Address:                  []string{"172.20.0.1/30"},
		MTU:                      9000,
		AutoRoute:                true,
		StrictRoute:              false,
		Stack:                    "mixed",
		Sniff:                    true,
		SniffOverrideDestination: true,
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

func buildRoute(routingCfg *RoutingConfig) SBRoute {
	rules := []SBRouteRule{
		{Protocol: "dns", Action: "hijack-dns"},
		// Локальные адреса всегда напрямую — LAN, loopback, link-local.
		// Это стандарт во всех proxy-клиентах (Clash Verge, Hiddify, Mihomo Party).
		{IPCIDR: privateIPRanges, Outbound: "direct"},
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
		AutoDetectInterface:   true,
		DefaultDomainResolver: "direct-dns",
	}
}
