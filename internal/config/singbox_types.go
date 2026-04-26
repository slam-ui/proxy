package config

const TunInterfaceName = "tun0"

// DataDir — папка для данных приложения (geosite .bin файлы, routing.json, app_rules.json).
// Располагается рядом с .exe, изолирует рабочие файлы от системных.
const DataDir = "data"

// DNSCacheFile — путь к файлу кэша DNS sing-box.
// Используется для удаления при ошибке "initialize cache-file: timeout".
const DNSCacheFile = DataDir + "/dns_cache.db"

// MinValidBinarySize — минимальный допустимый размер sing-box.exe.
// Файлы меньше 1 MB считаются повреждёнными или усечёнными.
const MinValidBinarySize = 1 * 1024 * 1024 // 1 MB

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
	// Strategy: ipv4_only — Запрещает DNS возвращать AAAA-записи.
	// Без этого sing-box пытается подключиться к IPv6-адресам напрямую,
	// что на машинах без IPv6 даёт "The requested address is not valid in its context".
	Strategy string `json:"strategy,omitempty"`
}

type SBDNSServer struct {
	Tag        string `json:"tag"`
	Type       string `json:"type"`
	Server     string `json:"server,omitempty"`
	ServerPort int    `json:"server_port,omitempty"`
	// Path — путь для DoH запросов, например "/dns-query".
	// Требуется для HTTPS DNS: server содержит только хост ("1.1.1.1"),
	// path — отдельное поле, иначе sing-box percent-кодирует слеш в server
	// и строит невалидный URL "https://1.1.1.1%2Fdns-query/dns-query".
	Path   string `json:"path,omitempty"`
	Detour string `json:"detour,omitempty"` // маршрутизировать DNS через указанный outbound
}

type SBDNSRule struct {
	Inbound []string `json:"inbound,omitempty"`
	Server  string   `json:"server"`
}

type SBInbound struct {
	Type          string   `json:"type"`
	Tag           string   `json:"tag"`
	Listen        string   `json:"listen,omitempty"`
	ListenPort    int      `json:"listen_port,omitempty"`
	InterfaceName string   `json:"interface_name,omitempty"`
	Address       []string `json:"address,omitempty"`
	MTU           int      `json:"mtu,omitempty"`
	AutoRoute     bool     `json:"auto_route,omitempty"`
	StrictRoute   bool     `json:"strict_route,omitempty"`
	Stack         string   `json:"stack,omitempty"`
	// RouteExcludeAddress — IP-адреса которые TUN-драйвер НЕ перехватывает.
	// Критично: без этого sing-box перехватывает собственные соединения к прокси-серверу
	// → routing loop (тысячи соединений по 500-600 байт на один IP).
	RouteExcludeAddress []string `json:"route_exclude_address,omitempty"`
	// Sniff и SniffOverrideDestination удалены: legacy inbound fields, deprecated в 1.11,
	// removed в 1.13. Sniffing настраивается через route rule {Action: "sniff"}.
}

// SBMultiplex конфигурация мультиплексирования соединений.
// Multiplex позволяет нескольким потокам данных использовать одно TLS соединение,
// устраняя overhead нового TLS хендшейка (~50-100мс) для каждого соединения.
// ВАЖНО: работает только если сервер поддерживает mux (sing-box / xray с mux).
type SBMultiplex struct {
	Enabled    bool   `json:"enabled"`
	Protocol   string `json:"protocol,omitempty"` // "smux" | "yamux" | "h2mux"
	MaxStreams int    `json:"max_streams,omitempty"`
	Padding    bool   `json:"padding,omitempty"`
}

type SBOutbound struct {
	Type       string       `json:"type"`
	Tag        string       `json:"tag"`
	Server     string       `json:"server,omitempty"`
	ServerPort int          `json:"server_port,omitempty"`
	UUID       string       `json:"uuid,omitempty"`
	Flow       string       `json:"flow,omitempty"`
	TLS        *SBTLS       `json:"tls,omitempty"`
	Multiplex  *SBMultiplex `json:"multiplex,omitempty"`
	// tcp_fast_open — валидный dial-field в sing-box v1.10+.
	// tcp_no_delay, tcp_keep_alive, connect_timeout удалены: не существуют в схеме
	// sing-box v1.13.5 и вызывают FATAL[0000] "json: unknown field" при старте.
	TCPFastOpen *bool `json:"tcp_fast_open,omitempty"`
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
	// CacheFile — персистентный кэш DNS между перезапусками sing-box.
	// Устраняет cold DNS lookup (~50-200мс) на первых запросах после рестарта.
	CacheFile *SBCacheFile `json:"cache_file,omitempty"`
}

// SBCacheFile включает персистентный кэш DNS (sing-box experimental).
type SBCacheFile struct {
	Enabled     bool   `json:"enabled"`
	Path        string `json:"path,omitempty"`
	CacheID     string `json:"cache_id,omitempty"`
	StoreFakeIP bool   `json:"store_fakeip,omitempty"`
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
	// FindProcess включает детектирование имени процесса для routing rules.
	// Обязательно для работы process_name правил — без этого sing-box не определяет
	// источник соединения и process_name никогда не матчится.
	FindProcess bool `json:"find_process,omitempty"`
}

type SBRouteRule struct {
	Protocol     string   `json:"protocol,omitempty"`
	Network      string   `json:"network,omitempty"` // "tcp" | "udp"
	Port         []uint16 `json:"port,omitempty"`    // порты для матчинга
	ProcessName  []string `json:"process_name,omitempty"`
	Domain       []string `json:"domain,omitempty"`
	DomainSuffix []string `json:"domain_suffix,omitempty"`
	IPCIDR       []string `json:"ip_cidr,omitempty"`
	Inbound      []string `json:"inbound,omitempty"`
	Action       string   `json:"action,omitempty"`
	Outbound     string   `json:"outbound,omitempty"`
	RuleSet      []string `json:"rule_set,omitempty"`
}
