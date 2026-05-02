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

func displayName(u *url.URL, fallback string) string {
	if u.Fragment != "" {
		if name, err := url.QueryUnescape(u.Fragment); err == nil && name != "" {
			return name
		}
		return u.Fragment
	}
	return fallback
}
