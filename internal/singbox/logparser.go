package singbox

import (
	"bufio"
	"strings"

	"proxyclient/internal/errcodes"
)

type Pattern struct {
	Needle string
	Code   errcodes.Code
	Stage  string
	Hint   string
}

var patterns = []Pattern{
	{Needle: "REALITY: processed invalid connection", Code: errcodes.RealityHandshakeFail, Stage: "handshake", Hint: "Проверьте Reality public key, short id и SNI."},
	{Needle: "tls: handshake failure", Code: errcodes.TLSHandshakeFailed, Stage: "handshake", Hint: "Проверьте SNI, ALPN и TLS-настройки сервера."},
	{Needle: "i/o timeout", Code: errcodes.TCPConnectFailed, Stage: "dialing", Hint: "Сервер не отвечает на порту. Попробуйте другой сервер или протокол."},
	{Needle: "connection refused", Code: errcodes.TCPConnectFailed, Stage: "dialing", Hint: "Порт закрыт или сервер не запущен."},
	{Needle: "unknown field", Code: errcodes.KeyParseError, Stage: "parsing", Hint: "Конфиг не соответствует схеме sing-box."},
	{Needle: "failed to start: open wintun", Code: errcodes.TUNAdapterFailed, Stage: "tunnel", Hint: "Проверьте права администратора и состояние Wintun."},
	{Needle: "authentication failed", Code: errcodes.AuthRejected, Stage: "handshake", Hint: "Проверьте UUID/password ключа."},
	{Needle: "unsupported transport", Code: errcodes.UnsupportedTransport, Stage: "parsing", Hint: "Этот transport пока не поддерживается клиентом."},
}

func ParseLogTail(logText string) *errcodes.Error {
	scanner := bufio.NewScanner(strings.NewReader(logText))
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	for i := len(lines) - 1; i >= 0; i-- {
		lower := strings.ToLower(lines[i])
		for _, p := range patterns {
			if strings.Contains(lower, strings.ToLower(p.Needle)) {
				return errcodes.New(p.Code, p.Stage, lines[i], p.Hint, nil)
			}
		}
	}
	if strings.TrimSpace(logText) == "" {
		return nil
	}
	return errcodes.New(errcodes.SingboxStartFailed, "startup", "sing-box stopped without a known signature", "Откройте лог sing-box и проверьте последние строки.", nil)
}
