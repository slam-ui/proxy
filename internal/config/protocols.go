package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

type ParsedServer struct {
	Proto       string
	DisplayName string
	Address     string
	Port        int
	Outbound    SBOutbound
}

func ParseServerContent(content string) (*ParsedServer, error) {
	content = strings.TrimSpace(content)
	switch {
	case strings.HasPrefix(content, "vless://"):
		params, err := ParseVLESSContent(content)
		if err != nil {
			return nil, err
		}
		if err := validateVLESSParams(params); err != nil {
			return nil, err
		}
		return &ParsedServer{
			Proto:       "vless",
			DisplayName: params.Address,
			Address:     params.Address,
			Port:        params.Port,
			Outbound:    buildVLESSOutbound(params),
		}, nil
	case strings.HasPrefix(content, "trojan://"):
		return parseTrojanURL(content)
	case strings.HasPrefix(content, "ss://"):
		return parseShadowsocksURL(content)
	case strings.HasPrefix(content, "hysteria2://") || strings.HasPrefix(content, "hy2://"):
		return parseHysteria2URL(content)
	case strings.HasPrefix(content, "tuic://"):
		return parseTUICURL(content)
	case strings.HasPrefix(content, "wireguard://"):
		return parseWireGuardURL(content)
	case strings.HasPrefix(content, "vmess://"):
		return parseVMessURL(content)
	case strings.Contains(content, "[Interface]") && strings.Contains(content, "[Peer]"):
		return parseWireGuardConf(content)
	default:
		return nil, fmt.Errorf("unsupported server protocol")
	}
}

type vmessJSON struct {
	V        string `json:"v"`
	PS       string `json:"ps"`
	Add      string `json:"add"`
	Port     string `json:"port"`
	ID       string `json:"id"`
	AID      string `json:"aid"`
	Security string `json:"scy"`
	Net      string `json:"net"`
	Type     string `json:"type"`
	Host     string `json:"host"`
	Path     string `json:"path"`
	TLS      string `json:"tls"`
	SNI      string `json:"sni"`
	ALPN     string `json:"alpn"`
	FP       string `json:"fp"`
}

func parseVMessURL(raw string) (*ParsedServer, error) {
	encoded := strings.TrimPrefix(raw, "vmess://")
	data, err := decodeMaybeBase64(encoded)
	if err != nil {
		return nil, fmt.Errorf("vmess base64: %w", err)
	}
	var v vmessJSON
	if err := json.Unmarshal([]byte(data), &v); err != nil {
		return nil, fmt.Errorf("vmess json: %w", err)
	}
	if v.Add == "" || v.Port == "" || v.ID == "" || v.Net == "" {
		return nil, fmt.Errorf("vmess add, port, id and net are required")
	}
	port, err := strconv.Atoi(v.Port)
	if err != nil || port <= 0 || port > 65535 {
		return nil, fmt.Errorf("vmess port is invalid")
	}
	aid := intQuery(v.AID)
	if aid > 0 {
		return nil, fmt.Errorf("vmess alterId > 0 is deprecated and not supported")
	}
	security := firstNonEmpty(v.Security, "auto")
	switch security {
	case "auto", "aes-128-gcm", "chacha20-poly1305", "none":
	default:
		return nil, fmt.Errorf("unsupported vmess security %q", security)
	}
	netType := strings.ToLower(v.Net)
	if netType == "kcp" || netType == "mkcp" {
		return nil, fmt.Errorf("vmess mKCP transport is not supported")
	}
	params := &VLESSParams{
		Address:     v.Add,
		Port:        port,
		Type:        netType,
		HeaderType:  strings.ToLower(v.Type),
		Path:        v.Path,
		Host:        splitQueryList(v.Host),
		ServiceName: v.Path,
		EarlyData:   0,
	}
	var tls *SBTLS
	if strings.EqualFold(v.TLS, "tls") {
		alpn := parseALPN(v.ALPN)
		if len(alpn) == 0 {
			alpn = []string{"h2", "http/1.1"}
		}
		tls = &SBTLS{
			Enabled:    true,
			ServerName: firstNonEmpty(v.SNI, v.Host, v.Add),
			ALPN:       alpn,
			UTLS:       &SBUTLS{Enabled: true, Fingerprint: firstNonEmpty(v.FP, "chrome")},
		}
	}
	out := SBOutbound{
		Type:         "vmess",
		Tag:          "proxy-out",
		Server:       v.Add,
		ServerPort:   port,
		UUID:         v.ID,
		Security:     security,
		AlterID:      0,
		TLS:          tls,
		Transport:    buildTransport(params),
		TCPFastOpen:  ptrBool(true),
		TCPMultiPath: true,
	}
	return &ParsedServer{Proto: "vmess", DisplayName: firstNonEmpty(v.PS, v.Add), Address: v.Add, Port: port, Outbound: out}, nil
}

func parseTrojanURL(raw string) (*ParsedServer, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("trojan parse: %w", err)
	}
	password, _ := u.User.Password()
	if password == "" {
		password = u.User.Username()
	}
	if password == "" {
		return nil, fmt.Errorf("trojan password is required")
	}
	host := u.Hostname()
	port, err := strconv.Atoi(u.Port())
	if err != nil || port <= 0 || port > 65535 {
		return nil, fmt.Errorf("trojan port is invalid")
	}
	q := u.Query()
	if sec := strings.ToLower(q.Get("security")); sec != "" && sec != "tls" {
		return nil, fmt.Errorf("trojan requires tls security")
	}
	if q.Get("pbk") != "" || strings.EqualFold(q.Get("security"), "reality") {
		return nil, fmt.Errorf("trojan reality is not supported")
	}
	tls := &SBTLS{
		Enabled:    true,
		ServerName: firstNonEmpty(q.Get("sni"), q.Get("peer"), host),
		UTLS:       &SBUTLS{Enabled: true, Fingerprint: firstNonEmpty(q.Get("fp"), "chrome")},
		ALPN:       parseALPN(q.Get("alpn")),
		Insecure:   parseBoolQuery(firstNonEmpty(q.Get("allowInsecure"), q.Get("insecure"))),
	}
	if len(tls.ALPN) == 0 {
		tls.ALPN = []string{"h2", "http/1.1"}
	}
	params := &VLESSParams{
		Address:     host,
		Port:        port,
		SNI:         tls.ServerName,
		Type:        strings.ToLower(firstNonEmpty(q.Get("type"), q.Get("net"), "tcp")),
		HeaderType:  strings.ToLower(q.Get("headerType")),
		Path:        q.Get("path"),
		Host:        splitQueryList(q.Get("host")),
		ServiceName: q.Get("serviceName"),
		EarlyData:   intQuery(q.Get("ed")),
	}
	out := SBOutbound{
		Type:         "trojan",
		Tag:          "proxy-out",
		Server:       host,
		ServerPort:   port,
		Password:     password,
		TLS:          tls,
		Transport:    buildTransport(params),
		TCPFastOpen:  ptrBool(true),
		TCPMultiPath: true,
	}
	return &ParsedServer{Proto: "trojan", DisplayName: displayName(u, host), Address: host, Port: port, Outbound: out}, nil
}

func parseShadowsocksURL(raw string) (*ParsedServer, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("shadowsocks parse: %w", err)
	}
	host := u.Hostname()
	port, err := strconv.Atoi(u.Port())
	if err != nil || port <= 0 || port > 65535 {
		return nil, fmt.Errorf("shadowsocks port is invalid")
	}
	method, password, err := parseSSUserInfo(u)
	if err != nil {
		return nil, err
	}
	if !supportedSSMethod(method) {
		return nil, fmt.Errorf("unsupported shadowsocks method %q", method)
	}
	q := u.Query()
	out := SBOutbound{
		Type:       "shadowsocks",
		Tag:        "proxy-out",
		Server:     host,
		ServerPort: port,
		Method:     method,
		Password:   password,
		Plugin:     q.Get("plugin"),
		PluginOpts: q.Get("plugin-opts"),
	}
	return &ParsedServer{Proto: "shadowsocks", DisplayName: displayName(u, host), Address: host, Port: port, Outbound: out}, nil
}

func parseHysteria2URL(raw string) (*ParsedServer, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("hysteria2 parse: %w", err)
	}
	password := u.User.Username()
	if password == "" {
		return nil, fmt.Errorf("hysteria2 password is required")
	}
	host := u.Hostname()
	port, err := strconv.Atoi(u.Port())
	if err != nil || port <= 0 || port > 65535 {
		return nil, fmt.Errorf("hysteria2 port is invalid")
	}
	q := u.Query()
	sni := firstNonEmpty(q.Get("sni"), q.Get("peer"), host)
	obfsType := q.Get("obfs")
	var obfs *SBObfs
	if obfsType != "" {
		if obfsType != "salamander" {
			return nil, fmt.Errorf("unsupported hysteria2 obfs %q", obfsType)
		}
		obfs = &SBObfs{Type: obfsType, Password: firstNonEmpty(q.Get("obfs-password"), q.Get("obfs_password"))}
	}
	out := SBOutbound{
		Type:       "hysteria2",
		Tag:        "proxy-out",
		Server:     host,
		ServerPort: port,
		Password:   password,
		Obfs:       obfs,
		TLS: &SBTLS{
			Enabled:    true,
			ServerName: sni,
			ALPN:       []string{"h3"},
			Insecure:   parseBoolQuery(firstNonEmpty(q.Get("insecure"), q.Get("allowInsecure"))),
		},
		UpMbps:   intQueryDefault(firstNonEmpty(q.Get("up"), q.Get("upmbps")), 50),
		DownMbps: intQueryDefault(firstNonEmpty(q.Get("down"), q.Get("downmbps")), 200),
		MTU:      intQueryDefault(q.Get("mtu"), 1200),
	}
	return &ParsedServer{Proto: "hysteria2", DisplayName: displayName(u, host), Address: host, Port: port, Outbound: out}, nil
}

func parseTUICURL(raw string) (*ParsedServer, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("tuic parse: %w", err)
	}
	uuid := u.User.Username()
	password, _ := u.User.Password()
	if uuid == "" || password == "" {
		return nil, fmt.Errorf("tuic uuid and password are required")
	}
	host := u.Hostname()
	port, err := strconv.Atoi(u.Port())
	if err != nil || port <= 0 || port > 65535 {
		return nil, fmt.Errorf("tuic port is invalid")
	}
	q := u.Query()
	cc := firstNonEmpty(q.Get("congestion_control"), q.Get("congestion"), "bbr")
	switch cc {
	case "bbr", "cubic", "new_reno":
	default:
		return nil, fmt.Errorf("unsupported tuic congestion_control %q", cc)
	}
	relay := firstNonEmpty(q.Get("udp_relay_mode"), "native")
	switch relay {
	case "native", "quic":
	default:
		return nil, fmt.Errorf("unsupported tuic udp_relay_mode %q", relay)
	}
	alpn := parseALPN(q.Get("alpn"))
	if len(alpn) == 0 {
		alpn = []string{"h3"}
	}
	out := SBOutbound{
		Type:              "tuic",
		Tag:               "proxy-out",
		Server:            host,
		ServerPort:        port,
		UUID:              uuid,
		Password:          password,
		CongestionControl: cc,
		UDPRelayMode:      relay,
		TLS: &SBTLS{
			Enabled:    true,
			ServerName: firstNonEmpty(q.Get("sni"), q.Get("peer"), host),
			ALPN:       alpn,
			Insecure:   parseBoolQuery(firstNonEmpty(q.Get("insecure"), q.Get("allowInsecure"))),
		},
	}
	return &ParsedServer{Proto: "tuic", DisplayName: displayName(u, host), Address: host, Port: port, Outbound: out}, nil
}

type wireGuardParams struct {
	Name          string
	PrivateKey    string
	Address       []string
	DNS           []string
	MTU           int
	PeerPublicKey string
	PresharedKey  string
	Endpoint      string
	Reserved      []int
}

func parseWireGuardConf(content string) (*ParsedServer, error) {
	var section string
	var p wireGuardParams
	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.Trim(line, "[]"))
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch section + "." + key {
		case "interface.privatekey":
			p.PrivateKey = value
		case "interface.address":
			p.Address = splitQueryList(value)
		case "interface.dns":
			p.DNS = splitQueryList(value)
		case "interface.mtu":
			p.MTU = intQuery(value)
		case "peer.publickey":
			if p.PeerPublicKey == "" {
				p.PeerPublicKey = value
			}
		case "peer.presharedkey":
			if p.PresharedKey == "" {
				p.PresharedKey = value
			}
		case "peer.endpoint":
			if p.Endpoint == "" {
				p.Endpoint = value
			}
		}
	}
	return buildWireGuardServer(p)
}

func parseWireGuardURL(raw string) (*ParsedServer, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("wireguard parse: %w", err)
	}
	q := u.Query()
	p := wireGuardParams{
		Name:          displayName(u, u.Hostname()),
		PrivateKey:    u.User.Username(),
		Address:       splitQueryList(q.Get("address")),
		MTU:           intQuery(q.Get("mtu")),
		PeerPublicKey: firstNonEmpty(q.Get("publickey"), q.Get("peer_public_key")),
		PresharedKey:  firstNonEmpty(q.Get("presharedkey"), q.Get("pre_shared_key")),
		Endpoint:      net.JoinHostPort(u.Hostname(), u.Port()),
	}
	if reserved := splitQueryList(q.Get("reserved")); len(reserved) == 3 {
		p.Reserved = []int{intQuery(reserved[0]), intQuery(reserved[1]), intQuery(reserved[2])}
	}
	return buildWireGuardServer(p)
}

func buildWireGuardServer(p wireGuardParams) (*ParsedServer, error) {
	if p.PrivateKey == "" {
		return nil, fmt.Errorf("wireguard private key is required")
	}
	if len(p.Address) == 0 {
		return nil, fmt.Errorf("wireguard address is required")
	}
	if p.PeerPublicKey == "" {
		return nil, fmt.Errorf("wireguard peer public key is required")
	}
	host, portText, err := net.SplitHostPort(p.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("wireguard endpoint must be host:port")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 || port > 65535 {
		return nil, fmt.Errorf("wireguard endpoint port is invalid")
	}
	if p.MTU <= 0 {
		p.MTU = 1408
	}
	out := SBOutbound{
		Type:            "wireguard",
		Tag:             "proxy-out",
		Server:          host,
		ServerPort:      port,
		SystemInterface: ptrBool(false),
		InterfaceName:   "wg0",
		LocalAddress:    p.Address,
		PrivateKey:      p.PrivateKey,
		PeerPublicKey:   p.PeerPublicKey,
		PreSharedKey:    p.PresharedKey,
		MTU:             p.MTU,
		Reserved:        p.Reserved,
	}
	return &ParsedServer{Proto: "wireguard", DisplayName: firstNonEmpty(p.Name, host), Address: host, Port: port, Outbound: out}, nil
}

func parseSSUserInfo(u *url.URL) (string, string, error) {
	user := u.User.Username()
	if pass, ok := u.User.Password(); ok {
		method, err := decodeMaybeBase64(user)
		if err != nil {
			return "", "", err
		}
		return method, pass, nil
	}
	decoded, err := decodeMaybeBase64(user)
	if err != nil {
		return "", "", err
	}
	method, password, ok := strings.Cut(decoded, ":")
	if !ok || method == "" || password == "" {
		return "", "", fmt.Errorf("invalid shadowsocks userinfo")
	}
	return method, password, nil
}

func decodeMaybeBase64(s string) (string, error) {
	if strings.Contains(s, ":") {
		return s, nil
	}
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return string(b), nil
		}
	}
	return "", fmt.Errorf("invalid base64")
}

func supportedSSMethod(method string) bool {
	switch strings.ToLower(method) {
	case "aes-128-gcm", "aes-256-gcm", "chacha20-poly1305", "2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305":
		return true
	default:
		return false
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func intQuery(v string) int {
	n, _ := strconv.Atoi(v)
	return n
}

func intQueryDefault(v string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func displayName(u *url.URL, fallback string) string {
	if u.Fragment != "" {
		if name, err := url.QueryUnescape(u.Fragment); err == nil && name != "" {
			return name
		}
		return u.Fragment
	}
	return fallback
}
