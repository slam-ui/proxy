package errcodes

import "proxyclient/internal/i18n"

type Template struct {
	Title   string   `json:"title"`
	Body    string   `json:"body"`
	Actions []string `json:"actions"`
}

var messagesRU = map[Code]Template{
	RealityHandshakeFail: {
		Title:   "Не удалось подключиться",
		Body:    "Сервер не ответил на Reality handshake. Проверьте public key, short id и SNI.",
		Actions: []string{"Retry", "Diagnose", "ChangeServer", "ShowLog"},
	},
	TLSHandshakeFailed: {
		Title:   "TLS handshake не прошёл",
		Body:    "Сервер отклонил TLS-соединение. Проверьте SNI, ALPN и сертификат сервера.",
		Actions: []string{"Retry", "Diagnose", "ShowLog"},
	},
	TCPConnectFailed: {
		Title:   "Сервер не отвечает",
		Body:    "Не удалось открыть TCP-соединение. Возможна блокировка IP или порта.",
		Actions: []string{"Retry", "ChangeServer", "Diagnose"},
	},
	DNSResolveFailed: {
		Title:   "DNS не разрешил сервер",
		Body:    "Hostname сервера не найден. Проверьте интернет и DNS-настройки.",
		Actions: []string{"Retry", "Diagnose"},
	},
	TUNAdapterFailed: {
		Title:   "Не поднялся TUN-адаптер",
		Body:    "Windows не дала создать сетевой адаптер. Перезапустите клиент от администратора.",
		Actions: []string{"Retry", "ShowLog"},
	},
	KeyParseError: {
		Title:   "Ключ не читается",
		Body:    "Ссылка или конфигурация содержит неподдерживаемые или неверные поля.",
		Actions: []string{"EditServer", "ShowLog"},
	},
	InternalError: {
		Title:   "Внутренняя ошибка",
		Body:    "Клиент получил непредвиденную ошибку. Откройте диагностический пакет.",
		Actions: []string{"Diagnose", "ShowLog"},
	},
}

func MessageRU(code Code) Template {
	return Message(i18n.LocaleRU, code)
}

func Message(locale i18n.Locale, code Code) Template {
	if i18n.NormalizeLocale(locale) == i18n.LocaleEN {
		if msg, ok := messagesEN[code]; ok {
			return msg
		}
		return messagesEN[InternalError]
	}
	if msg, ok := messagesRU[code]; ok {
		return msg
	}
	return messagesRU[InternalError]
}
