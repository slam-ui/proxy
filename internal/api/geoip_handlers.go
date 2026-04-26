package api

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// geoCache — in-memory кэш результатов GeoIP-определения.
// TTL 24 часа: IP-адрес меняет страну крайне редко, кэш снижает нагрузку DNS.
var (
	geoCacheMu sync.Mutex
	geoCache   = map[string]geoCacheEntry{}
)

type geoCacheEntry struct {
	cc      string // пустая строка = "не определено"
	expires time.Time
}

// SetupGeoIPRoutes регистрирует эндпоинт локального определения страны по хостнейму.
// Не требует внешних запросов — только DNS lookup + эвристики по hostname/IP.
func (s *Server) SetupGeoIPRoutes() {
	s.router.HandleFunc("/api/geoip", handleGeoIP).Methods("GET", "OPTIONS")
}

// handleGeoIP GET /api/geoip?host=38.244.128.202
// Возвращает {"country_code":"DE"} или {"country_code":""} если определить не удалось.
// Алгоритм:
//  1. Кэш (TTL 24h)
//  2. Паттерны в hostname (ccTLD, VPN-именование)
//  3. PTR-запрос (reverse DNS) — с таймаутом 1.5s
//  4. Для доменов: forward DNS → PTR
//  5. Fallback: ip-api.com (3s timeout)
//
// Общий бюджет запроса: 5 секунд.
// Frontend AbortController таймаут 8s — бюджет 5s гарантирует ответ до его истечения.
// Без таймаута PTR-lookup (net.LookupAddr) блокировал на 5–30с → ip-api.com никогда
// не вызывался → флаг страны не отображался.
func handleGeoIP(w http.ResponseWriter, r *http.Request) {
	host := strings.TrimSpace(r.URL.Query().Get("host"))
	if host == "" {
		respondGeoIP(w, "")
		return
	}

	// Убираем порт если есть
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}

	// Шаг 0: кэш
	geoCacheMu.Lock()
	if e, ok := geoCache[host]; ok && time.Now().Before(e.expires) {
		geoCacheMu.Unlock()
		respondGeoIP(w, e.cc)
		return
	}
	geoCacheMu.Unlock()

	// Ограничиваем весь resolve 5 секундами.
	// Это позволяет PTR-lookup попробоваться (~1.5s), и в случае неудачи
	// ip-api.com (3s) успеть ответить ДО того как frontend AbortController (8s) сработает.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	cc := resolveGeoIP(ctx, host)

	// Записываем в кэш: успех — TTL 24h, неудача — TTL 2m.
	// 2 минуты: короткий TTL для неудач позволяет повторить попытку быстро
	// (машина только что вышла в сеть, ip-api.com временно недоступен, и т.д.).
	// 30 минут было слишком агрессивно — флаг страны не показывался полчаса после
	// любой транзиентной ошибки сети при старте приложения.
	ttl := 24 * time.Hour
	if cc == "" {
		ttl = 2 * time.Minute
	}
	geoCacheMu.Lock()
	geoCache[host] = geoCacheEntry{cc: cc, expires: time.Now().Add(ttl)}
	geoCacheMu.Unlock()

	respondGeoIP(w, cc)
}

// resolveGeoIP — логика определения страны без кэша.
// Все DNS-операции выполняются с переданным ctx (с таймаутом из handleGeoIP).
func resolveGeoIP(ctx context.Context, host string) string {
	// Шаг 1: паттерны в hostname
	if cc := countryFromHostname(host); cc != "" {
		return cc
	}

	// Шаг 2: если это IP-адрес — PTR lookup
	ip := net.ParseIP(host)
	if ip != nil {
		// PTR-lookup с коротким таймаутом: если DNS медленный, не ждём его целиком.
		// Используем 1.5s для PTR — если не успел, переходим к ip-api.com (3s).
		ptrCtx, ptrCancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		cc := countryFromPTR(ptrCtx, ip)
		ptrCancel()
		if cc != "" {
			return cc
		}
		// Шаг 5 (fallback): ip-api.com когда PTR не помог
		if cc := countryFromIPAPI(ctx, ip.String()); cc != "" {
			return cc
		}
		return ""
	}

	// Шаг 3: доменное имя → resolve IP → PTR
	// Используем 1s таймаут для forward DNS чтобы оставить время ip-api.com
	dnsCtx, dnsCancel := context.WithTimeout(ctx, 1000*time.Millisecond)
	ips, err := net.DefaultResolver.LookupHost(dnsCtx, host)
	dnsCancel()
	if err != nil || len(ips) == 0 {
		// Если forward DNS завис — пробуем ip-api.com с hostname напрямую
		if cc := countryFromIPAPI(ctx, host); cc != "" {
			return cc
		}
		return ""
	}
	for _, ipStr := range ips {
		if resolved := net.ParseIP(ipStr); resolved != nil {
			ptrCtx, ptrCancel := context.WithTimeout(ctx, 800*time.Millisecond)
			cc := countryFromPTR(ptrCtx, resolved)
			ptrCancel()
			if cc != "" {
				return cc
			}
		}
	}
	// Fallback для домена: пробуем через ip-api.com первый резолвленный IP
	if len(ips) > 0 {
		if cc := countryFromIPAPI(ctx, ips[0]); cc != "" {
			return cc
		}
	}
	return ""
}

// countryFromIPAPI запрашивает страну через ip-api.com (бесплатный, без ключа, 45 req/min).
// Используется только когда PTR не содержит паттернов страны.
// Таймаут: минимум из (оставшегося времени ctx, 3s) — вписываемся в общий бюджет handleGeoIP.
func countryFromIPAPI(ctx context.Context, ipStr string) string {
	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet,
		"http://ip-api.com/json/"+ipStr+"?fields=countryCode", nil)
	if err != nil {
		return ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var d struct {
		CountryCode string `json:"countryCode"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 512)).Decode(&d); err != nil {
		return ""
	}
	if len(d.CountryCode) == 2 {
		return strings.ToUpper(d.CountryCode)
	}
	return ""
}

func respondGeoIP(w http.ResponseWriter, cc string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if cc != "" {
		_, _ = w.Write([]byte(`{"country_code":"` + cc + `"}`))
	} else {
		_, _ = w.Write([]byte(`{"country_code":""}`))
	}
}

// countryFromPTR делает reverse DNS и ищет паттерны страны в PTR-записи.
// Принимает ctx с таймаутом — без него net.LookupAddr может блокировать до 30s.
func countryFromPTR(ctx context.Context, ip net.IP) string {
	names, err := net.DefaultResolver.LookupAddr(ctx, ip.String())
	if err != nil || len(names) == 0 {
		return ""
	}
	for _, name := range names {
		if cc := countryFromHostname(name); cc != "" {
			return cc
		}
	}
	return ""
}

// countryFromHostname ищет страну по паттернам в hostname.
// Поддерживает:
//   - ccTLD: .de, .nl, .us, .uk, .fr, .ru, ...
//   - Инфиксы VPN-провайдеров: -de-, -nl-, -us-, de-, us-
//   - ISO-коды в доменных метках: de.server.com, nl1.vpn.net, de1.example.com
func countryFromHostname(host string) string {
	host = strings.ToLower(strings.TrimRight(host, "."))
	if host == "" {
		return ""
	}

	// ccTLD на конце (e.g. host.de, host.nl)
	if idx := strings.LastIndexByte(host, '.'); idx >= 0 {
		tld := host[idx+1:]
		if cc := tldToCC(tld); cc != "" {
			return cc
		}
	}

	// Анализируем каждую метку (label) в hostname
	labels := strings.Split(host, ".")
	for _, label := range labels {
		// Чистый двухбуквенный ISO код (de, nl, us, gb, ...)
		if len(label) == 2 {
			if cc := isoToCC(label); cc != "" {
				return cc
			}
		}
		// label начинается с ISO кода + цифра: de1, us2, nl3
		if len(label) >= 3 {
			prefix := label[:2]
			rest := label[2:]
			if isDigitOnly(rest) {
				if cc := isoToCC(prefix); cc != "" {
					return cc
				}
			}
		}
	}

	// Паттерны с дефисами: server-de-01, de-server, nl-vpn-1
	parts := strings.FieldsFunc(host, func(r rune) bool { return r == '-' || r == '_' })
	for _, p := range parts {
		if len(p) == 2 {
			if cc := isoToCC(p); cc != "" {
				return cc
			}
		}
		if len(p) >= 3 && isDigitOnly(p[2:]) {
			if cc := isoToCC(p[:2]); cc != "" {
				return cc
			}
		}
	}

	return ""
}

func isDigitOnly(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// tldToCC конвертирует ccTLD → ISO 3166-1 alpha-2.
// Только двухбуквенные ccTLD которые совпадают с кодом страны.
func tldToCC(tld string) string {
	// Большинство ccTLD совпадают с ISO кодом, кроме нескольких исключений.
	exceptions := map[string]string{
		"uk":   "GB", // .uk → United Kingdom (ISO: GB)
		"su":   "RU", // .su → Soviet Union (фактически RU)
		"ac":   "",   // Ascension Island — не страна VPN
		"io":   "",   // British Indian Ocean — используется как generic TLD
		"tv":   "",   // Tuvalu — используется как generic TLD
		"co":   "",   // Colombia — но .co используется как generic TLD
		"me":   "",   // Montenegro — но .me используется как generic TLD
		"ai":   "",   // Anguilla — generic TLD
		"app":  "",
		"net":  "",
		"com":  "",
		"org":  "",
		"xyz":  "",
		"info": "",
		"top":  "",
		"site": "",
	}
	if v, ok := exceptions[tld]; ok {
		return v
	}
	return isoToCC(tld)
}

// isoToCC проверяет является ли строка валидным ISO 3166-1 alpha-2 кодом страны
// и возвращает его в верхнем регистре. Поддерживает основные страны VPN-рынка.
func isoToCC(s string) string {
	s = strings.ToUpper(s)
	knownCodes := map[string]bool{
		"AF": true, "AL": true, "DZ": true, "AD": true, "AO": true,
		"AG": true, "AR": true, "AM": true, "AU": true, "AT": true,
		"AZ": true, "BS": true, "BH": true, "BD": true, "BB": true,
		"BY": true, "BE": true, "BZ": true, "BJ": true, "BT": true,
		"BO": true, "BA": true, "BW": true, "BR": true, "BN": true,
		"BG": true, "BF": true, "BI": true, "CV": true, "KH": true,
		"CM": true, "CA": true, "CF": true, "TD": true, "CL": true,
		"CN": true, "CO": true, "KM": true, "CD": true, "CG": true,
		"CR": true, "HR": true, "CU": true, "CY": true, "CZ": true,
		"DK": true, "DJ": true, "DM": true, "DO": true, "EC": true,
		"EG": true, "SV": true, "GQ": true, "ER": true, "EE": true,
		"SZ": true, "ET": true, "FJ": true, "FI": true, "FR": true,
		"GA": true, "GM": true, "GE": true, "DE": true, "GH": true,
		"GR": true, "GD": true, "GT": true, "GN": true, "GW": true,
		"GY": true, "HT": true, "HN": true, "HU": true, "IS": true,
		"IN": true, "ID": true, "IR": true, "IQ": true, "IE": true,
		"IL": true, "IT": true, "JM": true, "JP": true, "JO": true,
		"KZ": true, "KE": true, "KI": true, "KW": true, "KG": true,
		"LA": true, "LV": true, "LB": true, "LS": true, "LR": true,
		"LY": true, "LI": true, "LT": true, "LU": true, "MG": true,
		"MW": true, "MY": true, "MV": true, "ML": true, "MT": true,
		"MH": true, "MR": true, "MU": true, "MX": true, "FM": true,
		"MD": true, "MC": true, "MN": true, "ME": true, "MA": true,
		"MZ": true, "MM": true, "NA": true, "NR": true, "NP": true,
		"NL": true, "NZ": true, "NI": true, "NE": true, "NG": true,
		"MK": true, "NO": true, "OM": true, "PK": true, "PW": true,
		"PA": true, "PG": true, "PY": true, "PE": true, "PH": true,
		"PL": true, "PT": true, "QA": true, "RO": true, "RU": true,
		"RW": true, "KN": true, "LC": true, "VC": true, "WS": true,
		"SM": true, "ST": true, "SA": true, "SN": true, "RS": true,
		"SC": true, "SL": true, "SG": true, "SK": true, "SI": true,
		"SB": true, "SO": true, "ZA": true, "SS": true, "ES": true,
		"LK": true, "SD": true, "SR": true, "SE": true, "CH": true,
		"SY": true, "TW": true, "TJ": true, "TZ": true, "TH": true,
		"TL": true, "TG": true, "TO": true, "TT": true, "TN": true,
		"TR": true, "TM": true, "TV": true, "UG": true, "UA": true,
		"AE": true, "GB": true, "US": true, "UY": true, "UZ": true,
		"VU": true, "VE": true, "VN": true, "YE": true, "ZM": true,
		"ZW": true,
	}
	if knownCodes[s] {
		return s
	}
	return ""
}
