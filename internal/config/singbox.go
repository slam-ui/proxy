package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const TunInterfaceName = "tun0"

type SingBoxConfig struct {
	Log          SBLog          `json:"log"`
	DNS          SBDNS          `json:"dns"`
	Experimental SBExperimental `json:"experimental"`
	Inbounds     []SBInbound    `json:"inbounds"`
	Outbounds    []SBOutbound   `json:"outbounds"`
	Route        SBRoute        `json:"route"`
}

type SBLog struct {
	Level string `json:"level"`
}

type SBDNS struct {
	Servers []SBDNSServer `json:"servers"`
	Rules   []SBDNSRule   `json:"rules,omitempty"`
	Final   string        `json:"final,omitempty"`
}

type SBDNSServer struct {
	Tag        string `json:"tag"`
	Type       string `json:"type"`
	Server     string `json:"server,omitempty"`
	ServerPort int    `json:"server_port,omitempty"`
}

type SBDNSRule struct {
	Inbound []string `json:"inbound,omitempty"`
	Server  string   `json:"server"`
}

type SBInbound struct {
	Type                     string   `json:"type"`
	Tag                      string   `json:"tag"`
	Listen                   string   `json:"listen,omitempty"`
	ListenPort               int      `json:"listen_port,omitempty"`
	Sniff                    bool     `json:"sniff,omitempty"`
	SniffOverrideDestination bool     `json:"sniff_override_destination,omitempty"`
	InterfaceName            string   `json:"interface_name,omitempty"`
	Address                  []string `json:"address,omitempty"`
	MTU                      int      `json:"mtu,omitempty"`
	AutoRoute                bool     `json:"auto_route,omitempty"`
	StrictRoute              bool     `json:"strict_route,omitempty"`
	Stack                    string   `json:"stack,omitempty"`
}

type SBOutbound struct {
	Type       string `json:"type"`
	Tag        string `json:"tag"`
	Server     string `json:"server,omitempty"`
	ServerPort int    `json:"server_port,omitempty"`
	UUID       string `json:"uuid,omitempty"`
	Flow       string `json:"flow,omitempty"`
	TLS        *SBTLS `json:"tls,omitempty"`
}

type SBTLS struct {
	Enabled    bool       `json:"enabled"`
	ServerName string     `json:"server_name,omitempty"`
	Reality    *SBReality `json:"reality,omitempty"`
	UTLS       *SBUTLS    `json:"utls,omitempty"`
}

type SBReality struct {
	Enabled   bool   `json:"enabled"`
	PublicKey string `json:"public_key"`
	ShortID   string `json:"short_id"`
}

type SBUTLS struct {
	Enabled     bool   `json:"enabled"`
	Fingerprint string `json:"fingerprint"`
}

type SBRuleSet struct {
	Type   string `json:"type"`
	Tag    string `json:"tag"`
	Format string `json:"format"`
	Path   string `json:"path"`
}

// SBExperimental включает Clash-совместимый API для статистики трафика и соединений.
// Доступен на 127.0.0.1:9090 — используется нашим бэкендом для /api/stats и /api/connections.
type SBExperimental struct {
	ClashAPI SBClashAPI `json:"clash_api"`
}

type SBClashAPI struct {
	ExternalController string `json:"external_controller"`
	Secret             string `json:"secret"`
}

type SBRoute struct {
	Rules                 []SBRouteRule `json:"rules,omitempty"`
	RuleSet               []SBRuleSet   `json:"rule_set,omitempty"`
	Final                 string        `json:"final"`
	AutoDetectInterface   bool          `json:"auto_detect_interface"`
	DefaultDomainResolver string        `json:"default_domain_resolver,omitempty"`
}

type SBRouteRule struct {
	Protocol     string   `json:"protocol,omitempty"`
	ProcessName  []string `json:"process_name,omitempty"`
	Domain       []string `json:"domain,omitempty"`
	DomainSuffix []string `json:"domain_suffix,omitempty"`
	IPCIDR       []string `json:"ip_cidr,omitempty"`
	Inbound      []string `json:"inbound,omitempty"`
	Action       string   `json:"action,omitempty"`
	Outbound     string   `json:"outbound,omitempty"`
	RuleSet      []string `json:"rule_set,omitempty"`
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
				ExternalController: "127.0.0.1:9090",
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
				ListenPort:               10807,
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

	// Атомарная запись: tmp + rename, как в SaveRoutingConfig и fileStorage.Save.
	tmp := outputPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("не удалось записать временный конфиг: %w", err)
	}
	if err := os.Rename(tmp, outputPath); err != nil {
		_ = os.Remove(tmp)
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

func buildRoute(routingCfg *RoutingConfig) SBRoute {
	rules := []SBRouteRule{
		{Protocol: "dns", Action: "hijack-dns"},
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
			path := tag + ".bin"
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
