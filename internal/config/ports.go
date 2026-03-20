package config

// Адреса и порты всех компонентов приложения.
//
// Все три порта определены здесь — единственное место для изменения.
// Менять в одном месте: при конфликте порта достаточно обновить одну константу.
const (
	// ProxyPort — HTTP inbound порт sing-box. Системный прокси Windows указывает сюда.
	ProxyPort = 10807
	// ProxyAddr — полный адрес HTTP inbound sing-box.
	ProxyAddr = "127.0.0.1:10807"

	// ClashAPIPort — порт Clash-совместимого API sing-box (статистика, соединения).
	ClashAPIPort = 9090
	// ClashAPIAddr — полный адрес Clash API sing-box.
	ClashAPIAddr = "127.0.0.1:9090"
	// ClashAPIBase — базовый URL для запросов к Clash API.
	ClashAPIBase = "http://" + ClashAPIAddr

	// APIPort — порт HTTP API самого proxy-client (веб-интерфейс).
	APIPort = 8080
	// APIAddress — адрес для listen HTTP API proxy-client.
	APIAddress = ":8080"
)
