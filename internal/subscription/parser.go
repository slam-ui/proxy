package subscription

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const MaxBodyBytes = 1 << 20

type ServerEntry struct {
	Name string `json:"name"`
	URI  string `json:"uri"`
}

type Quota struct {
	Upload    int64     `json:"upload,omitempty"`
	Download  int64     `json:"download,omitempty"`
	Total     int64     `json:"total,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

func (q Quota) Used() int64 {
	return q.Upload + q.Download
}

type ParseResult struct {
	Servers  []ServerEntry `json:"servers"`
	Warnings []string      `json:"warnings,omitempty"`
}

type sip008Document struct {
	Version int            `json:"version"`
	Servers []sip008Server `json:"servers"`
}

type sip008Server struct {
	ID       string `json:"id"`
	Remarks  string `json:"remarks"`
	Server   string `json:"server"`
	ServerIP string `json:"server_ip"`
	Port     any    `json:"server_port"`
	Method   string `json:"method"`
	Password string `json:"password"`
	Plugin   string `json:"plugin"`
}

func ParseBody(body []byte, isSupported func(string) bool) ParseResult {
	content := strings.TrimSpace(strings.TrimPrefix(string(body), "\ufeff"))
	result := parseLines(content, isSupported)
	if len(result.Servers) > 0 {
		return result
	}

	if decoded, err := decodeBase64Subscription(content); err == nil {
		result = parseLines(decoded, isSupported)
		if len(result.Servers) > 0 {
			return result
		}
	}

	if sip, ok := parseSIP008(content); ok {
		return sip
	}

	if content != "" {
		result.Warnings = append(result.Warnings, "subscription has no supported server URI")
	}
	return result
}

func parseLines(content string, isSupported func(string) bool) ParseResult {
	var result ParseResult
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff"))
		if line == "" {
			continue
		}
		if isSupported(line) {
			result.Servers = append(result.Servers, ServerEntry{URI: line})
			continue
		}
		result.Warnings = append(result.Warnings, "skipped unsupported line")
	}
	return result
}

func decodeBase64Subscription(content string) (string, error) {
	compact := strings.Join(strings.Fields(content), "")
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(compact); err == nil {
			return string(b), nil
		}
	}
	return "", fmt.Errorf("subscription is not base64")
}

func parseSIP008(content string) (ParseResult, bool) {
	var doc sip008Document
	if err := json.Unmarshal([]byte(content), &doc); err != nil || len(doc.Servers) == 0 {
		return ParseResult{}, false
	}
	var result ParseResult
	for _, s := range doc.Servers {
		host := firstNonEmpty(s.Server, s.ServerIP)
		port := formatPort(s.Port)
		if host == "" || port == "" || s.Method == "" || s.Password == "" {
			result.Warnings = append(result.Warnings, "skipped incomplete SIP008 server")
			continue
		}
		uri := "ss://" + base64.RawURLEncoding.EncodeToString([]byte(s.Method+":"+s.Password)) + "@" + host + ":" + port
		if s.Plugin != "" {
			uri += "?plugin=" + s.Plugin
		}
		if s.Remarks != "" {
			uri += "#" + s.Remarks
		}
		result.Servers = append(result.Servers, ServerEntry{Name: firstNonEmpty(s.Remarks, s.ID, host), URI: uri})
	}
	return result, true
}

func formatPort(v any) string {
	switch p := v.(type) {
	case float64:
		if p <= 0 || p > 65535 || p != float64(int(p)) {
			return ""
		}
		return strconv.Itoa(int(p))
	case string:
		if n, err := strconv.Atoi(p); err == nil && n > 0 && n <= 65535 {
			return p
		}
	}
	return ""
}

func ParseUserInfoHeader(header string) Quota {
	var q Quota
	for _, part := range strings.Split(header, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil || n < 0 {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "upload":
			q.Upload = n
		case "download":
			q.Download = n
		case "total":
			q.Total = n
		case "expire":
			if n > 0 {
				q.ExpiresAt = time.Unix(n, 0).UTC()
			}
		}
	}
	return q
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
