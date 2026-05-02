package config

import (
	"encoding/base64"
	"fmt"
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
	default:
		return nil, fmt.Errorf("unsupported server protocol")
	}
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
