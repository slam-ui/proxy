# IMPROVEMENTS_PROMPT.md
# Полный план улучшений proxy-client — только клиентские изменения

## Контекст проекта

Go Windows-клиент `proxyclient` (module: `proxyclient`).
VLESS/Reality + TUN через sing-box **v1.13.7** (pinned). WebView2 UI.
Рабочая директория: `c:\Users\13372\GolandProjects\proxy`.

### Ключевые файлы

| Файл | Роль |
|------|------|
| `internal/config/singbox_types.go` | Структуры sing-box конфига |
| `internal/config/singbox_builder.go` | `buildVLESSOutbound`, `buildRoute`, `buildDNSConfig`, `buildSingBoxConfig` |
| `internal/config/vless.go` | `VLESSParams`, парсинг VLESS URL |
| `internal/config/tun.go` | `DNSConfig`, `RoutingRule`, `RoutingConfig` |
| `internal/config/ports.go` | Константы: `ClashAPIAddr`, `ClashAPIBase`, `ProxyPort` |
| `internal/api/server.go` | HTTP сервер; `switchClashMode` (~стр.558); `clashAPIURL` (стр.28) |
| `internal/api/diag_handlers.go` | Clash API запросы `/traffic` (~стр.94), `/connections` (~стр.343) |
| `internal/api/servers_handlers.go` | `handleAutoConnect` (~стр.399), `handleConnect`, запись secret.key |
| `internal/api/tun_handlers.go` | `TriggerApply`, `handleBulkReplaceRules` |
| `internal/api/profile_handlers.go` | Профили правил маршрутизации |
| `internal/xray/manager.go` | `Manager` interface (стр.142), `manager` struct (стр.269) |
| `internal/xray/healthcheck.go` | `HealthChecker`, `RecordError` (стр.61) |
| `internal/tray/tray.go` | Системный трей: `SetEnabled`, `SetActiveServer` |
| `cmd/proxy-client/app.go` | `AppConfig`, `finalizeStartup`, lifecycle |

### Обязательные правила (нельзя нарушать)

1. Не ломать тесты: `handlers_test.go`, `singbox_test.go`, `singbox_extra_test.go`,
   `servers_handlers_test.go`, `server_lifecycle_test.go`, `tun_handlers_test.go`
2. Windows-only файл → обязателен stub `*_other.go` с `//go:build !windows`
3. `unsafe.Pointer` — только в том же выражении `.Call(uintptr(unsafe.Pointer(&x)))`;
   `runtime.KeepAlive(&x)` после вызова если данные нужны дальше
4. Для критичных файлов — только `fileutil.WriteAtomic`, не `os.WriteFile`
5. Не восстанавливать TURN/watchdog — удалено навсегда
6. Новые HTTP-маршруты — только через `SetupFeatureRoutes()` в `server.go`
7. Частые poll-эндпоинты (>1 req/s) — добавлять в `s.addSilentPath()`
8. После каждого изменения: `go build ./...` и `go test ./...` должны пройти

---

## ГРУППА А — Безопасность

### А1. Clash API Secret

**Проблема:** `SBClashAPI.Secret = ""` — Clash API на `127.0.0.1:9090` без авторизации.
Любой локальный процесс может управлять туннелем, смотреть соединения, менять правила.

**А1.1** В `internal/config/singbox_builder.go` добавить в imports
`"crypto/rand"`, `"encoding/hex"`, `"sync"`. Добавить после существующих `var`:

```go
var (
    clashSecretOnce sync.Once
    clashSecret     string
)

// ClashAPISecret возвращает cryptographically random секрет для Clash API.
// Генерируется один раз за жизнь процесса через crypto/rand.
func ClashAPISecret() string {
    clashSecretOnce.Do(func() {
        b := make([]byte, 32)
        if _, err := rand.Read(b); err != nil {
            clashSecret = fmt.Sprintf("fallback-%d", time.Now().UnixNano())
            return
        }
        clashSecret = hex.EncodeToString(b)
    })
    return clashSecret
}
```

**А1.2** В `buildSingBoxConfig` (~стр.196):
```go
// БЫЛО: Secret: ""
// СТАЛО:
Secret: ClashAPISecret(),
```

**А1.3** В `internal/api/server.go`, `switchClashMode` (~стр.564),
после `req.Header.Set("Content-Type", "application/json")`:
```go
req.Header.Set("Authorization", "Bearer "+config.ClashAPISecret())
```

**А1.4** В `internal/api/diag_handlers.go` найти оба запроса к Clash API
(~стр.94 `/traffic` и ~стр.343 `/connections`), после `http.NewRequestWithContext(...)`:
```go
req.Header.Set("Authorization", "Bearer "+config.ClashAPISecret())
```

---

### А2. DPAPI шифрование secret.key

**Проблема:** VLESS URL с UUID хранится plaintext рядом с `.exe`.
При компрометации директории — UUID раскрывается.

**А2.1** Создать `internal/dpapi/dpapi_windows.go`:
```go
//go:build windows

package dpapi

import (
    "fmt"
    "runtime"
    "unsafe"
    "golang.org/x/sys/windows"
)

var (
    crypt32           = windows.NewLazySystemDLL("crypt32.dll")
    procProtectData   = crypt32.NewProc("CryptProtectData")
    procUnprotectData = crypt32.NewProc("CryptUnprotectData")
)

type dataBlob struct {
    cbData uint32
    pbData *byte
}

func newBlob(d []byte) *dataBlob {
    if len(d) == 0 {
        return &dataBlob{}
    }
    return &dataBlob{cbData: uint32(len(d)), pbData: &d[0]}
}

func Encrypt(data []byte) ([]byte, error) {
    in := newBlob(data)
    var out dataBlob
    r, _, err := procProtectData.Call(
        uintptr(unsafe.Pointer(in)), 0, 0, 0, 0, 0,
        uintptr(unsafe.Pointer(&out)),
    )
    runtime.KeepAlive(in)
    if r == 0 {
        return nil, fmt.Errorf("CryptProtectData: %w", err)
    }
    defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.pbData)))
    result := make([]byte, out.cbData)
    copy(result, unsafe.Slice(out.pbData, out.cbData))
    return result, nil
}

func Decrypt(data []byte) ([]byte, error) {
    in := newBlob(data)
    var out dataBlob
    r, _, err := procUnprotectData.Call(
        uintptr(unsafe.Pointer(in)), 0, 0, 0, 0, 0,
        uintptr(unsafe.Pointer(&out)),
    )
    runtime.KeepAlive(in)
    if r == 0 {
        return nil, fmt.Errorf("CryptUnprotectData: %w", err)
    }
    defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.pbData)))
    result := make([]byte, out.cbData)
    copy(result, unsafe.Slice(out.pbData, out.cbData))
    return result, nil
}
```

**А2.2** Создать `internal/dpapi/dpapi_other.go`:
```go
//go:build !windows

package dpapi

func Encrypt(data []byte) ([]byte, error) { return data, nil }
func Decrypt(data []byte) ([]byte, error) { return data, nil }
```

**А2.3** В `internal/config/vless.go` добавить в imports
`"encoding/base64"` и `"proxyclient/internal/dpapi"`:

```go
const dpapiMagic = "DPAPI:"

// ReadSecretKey читает secret.key с поддержкой DPAPI и plaintext форматов.
func ReadSecretKey(path string) (string, error) {
    data, err := os.ReadFile(path)
    if err != nil {
        return "", err
    }
    content := strings.TrimSpace(string(data))
    if strings.HasPrefix(content, dpapiMagic) {
        enc, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(content, dpapiMagic))
        if err != nil {
            return "", fmt.Errorf("dpapi base64: %w", err)
        }
        plain, err := dpapi.Decrypt(enc)
        if err != nil {
            return "", fmt.Errorf("dpapi decrypt: %w", err)
        }
        return strings.TrimSpace(string(plain)), nil
    }
    return content, nil
}

// WriteSecretKey записывает VLESS URL в secret.key через DPAPI.
// При ошибке DPAPI — записывает plaintext (соединение важнее хранения).
func WriteSecretKey(path, vlessURL string) error {
    enc, err := dpapi.Encrypt([]byte(vlessURL))
    var content []byte
    if err != nil {
        content = []byte(vlessURL)
    } else {
        content = []byte(dpapiMagic + base64.StdEncoding.EncodeToString(enc))
    }
    return fileutil.WriteAtomic(path, content, 0600)
}
```

Изменить `readAndParseVLESS`:
```go
func readAndParseVLESS(secretPath string) (*VLESSParams, error) {
    content, err := ReadSecretKey(secretPath)
    if err != nil {
        return nil, fmt.Errorf("не удалось прочитать '%s': %w", secretPath, err)
    }
    return parseVLESSContentInternal(content)
}
```

**А2.4** В `internal/api/servers_handlers.go` в `handleConnect` найти запись
`secret.key` и заменить на:
```go
if err := config.WriteSecretKey(h.secretKey, vlessURL); err != nil { ... }
```

---

### А3. Multiplex Padding

**Проблема:** `Padding: false` — DPI профилирует трафик по длинам фреймов h2mux.
Сервер уже поддерживает h2mux (multiplex уже включён в конфиге) — Padding прозрачен
для приёмника, изменение только клиентское.

В `internal/config/singbox_builder.go`, `buildVLESSOutbound`:
```go
out.Multiplex = &SBMultiplex{
    Enabled:    true,
    Protocol:   "h2mux",
    MaxStreams: 8,
    Padding:    true, // рандомизирует длины фреймов — DPI не профилирует
}
```

---

### А4. TLS MinVersion для plain TLS

**Проблема:** без явного ограничения возможен TLS 1.2 с уязвимыми шифрами.

В `internal/config/singbox_types.go` в структуру `SBTLS` добавить:
```go
MinVersion string `json:"min_version,omitempty"` // "1.3"
```

В `buildVLESSOutbound`, после блока Reality:
```go
if strings.TrimSpace(params.PublicKey) == "" {
    tls.MinVersion = "1.3" // plain TLS — принудительно TLS 1.3
}
```

---

## ГРУППА Б — Защита от DPI

### Б1. TLS ClientHello Fragmentation

**Проблема:** Российские ТСПУ анализируют **первый TCP-пакет** для извлечения SNI.
Фрагментация ClientHello на 10–50 байт ломает этот механизм — DPI не успевает собрать.
Не требует изменений на сервере: сервер получает стандартный TLS, только фрагментированный.

**Б1.1** В `internal/config/singbox_types.go` добавить:
```go
// SBTLSFragment — фрагментация TLS ClientHello.
// Разбивает первый TLS-пакет на мелкие сегменты: ТСПУ не успевает извлечь SNI.
type SBTLSFragment struct {
    Enabled bool   `json:"enabled"`
    Size    string `json:"size,omitempty"`  // диапазон байт: "10-50"
    Sleep   string `json:"sleep,omitempty"` // задержка между фрагментами: "0-5ms"
}
```

В структуру `SBOutbound` добавить поля после `TCPFastOpen`:
```go
TCPMultiPath bool           `json:"tcp_multi_path,omitempty"` // MPTCP
TLSFragment  *SBTLSFragment `json:"tls_fragment,omitempty"`
```

**Б1.2** В `internal/config/vless.go` добавить в `VLESSParams`:
```go
Fragment     bool   // включена ли фрагментация; true по умолчанию
FragmentSize string // кастомный размер фрагмента, напр. "1-30"
```

В `parseVLESSContentInternal` (~стр.175) после заполнения `params`:
```go
fragParam := queryParams.Get("fragment")
params.Fragment = fragParam != "0" && fragParam != "false" // включено по умолчанию
params.FragmentSize = queryParams.Get("fragment_size")
```

**Б1.3** В `buildVLESSOutbound` после блока Multiplex:
```go
if params.Fragment {
    size := "10-50"
    if params.FragmentSize != "" {
        size = params.FragmentSize
    }
    out.TLSFragment = &SBTLSFragment{Enabled: true, Size: size, Sleep: "0-5ms"}
}
out.TCPMultiPath = true // MPTCP: несколько TCP путей, Windows 11+; на W10 автофаллбэк
```

---

### Б2. Custom ALPN + uTLS Fingerprint Selector

**Проблема:** ALPN не задан → sing-box выбирает произвольно, отличаясь от браузерного
fingerprint. uTLS fingerprint захардкожен в `"random"` без возможности выбора.

**Б2.1** В `internal/config/singbox_types.go`, структура `SBTLS`:
```go
type SBTLS struct {
    Enabled    bool       `json:"enabled"`
    ServerName string     `json:"server_name,omitempty"`
    Reality    *SBReality `json:"reality,omitempty"`
    UTLS       *SBUTLS    `json:"utls,omitempty"`
    ALPN       []string   `json:"alpn,omitempty"`       // новое поле
    MinVersion string     `json:"min_version,omitempty"` // новое поле
}
```

**Б2.2** В `internal/config/vless.go` добавить в `VLESSParams`:
```go
Fingerprint string // ?fp=chrome|firefox|safari|edge|ios|android|random
```

В `parseVLESSContentInternal`:
```go
params.Fingerprint = queryParams.Get("fp")
```

Добавить метод:
```go
func (p *VLESSParams) UTLSFingerprint() string {
    if p.Fingerprint != "" {
        return p.Fingerprint
    }
    return "random"
}
```

**Б2.3** В `buildVLESSOutbound` изменить создание `tls`:
```go
tls := &SBTLS{
    Enabled:    true,
    ServerName: params.SNI,
    ALPN:       []string{"h2", "http/1.1"}, // имитация браузерного fingerprint
    UTLS:       &SBUTLS{Enabled: true, Fingerprint: params.UTLSFingerprint()},
}
```

---

### Б3. STUN / WebRTC блокировка

**Проблема:** браузеры биндят WebRTC UDP-сокеты на физический интерфейс до захвата TUN —
реальный IP утекает через UDP 3478 даже при включённом VPN.

В `internal/config/singbox_builder.go`, `buildRoute`, после правила
`{IPCIDR: telegramCIDRRanges, Outbound: "proxy-out"}`:

```go
// WebRTC/STUN leak prevention: блокируем UDP к STUN серверам.
{
    Network: "udp",
    Port:    []uint16{3478, 3479, 5349},
    DomainSuffix: []string{
        "stun.l.google.com", "stun.cloudflare.com", "stun.ekiga.net",
        "stun.ideasip.com",  "stun.softjoys.com",   "stun.voiparound.com",
        "stun.voipbuster.com", "stun.voipstunt.com", "stun.voxgratia.org",
    },
    Action: "reject",
},
{
    Network:      "tcp",
    Port:         []uint16{3478, 3479, 5349},
    DomainSuffix: []string{"stun.l.google.com", "stun.cloudflare.com"},
    Action:       "reject",
},
```

---

### Б4. DNS Fallback цепочка

**Проблема:** один remote DoH сервер — при блокировке DNS падает полностью.

**Б4.1** В `internal/config/tun.go`, `DNSConfig`:
```go
type DNSConfig struct {
    RemoteDNS         string   `json:"remote_dns"`
    DirectDNS         string   `json:"direct_dns"`
    RemoteDNSFallback []string `json:"remote_dns_fallback,omitempty"`
}
```

`DefaultDNSConfig()` — добавить fallback:
```go
RemoteDNSFallback: []string{
    "https://8.8.8.8/dns-query",
    "https://9.9.9.9/dns-query",
    "quic://dns.adguard.com",
},
```

**Б4.2** В `buildDNSConfig` после создания основного `remote` сервера:
```go
for i, fb := range dnsCfg.RemoteDNSFallback {
    fbServer, fbPath, fbType := parseDNSURL(fb)
    tag := fmt.Sprintf("remote-fb%d", i+1)
    servers = append(servers, SBDNSServer{
        Tag:    tag,
        Type:   fbType,
        Server: fbServer,
        Path:   fbPath,
        Detour: "proxy-out",
    })
}
```

---

### Б5. Блокировка QUIC/HTTP3 (UDP 443)

**Проблема:** браузеры используют QUIC (HTTP/3) по UDP 443. В некоторых конфигурациях
TUN QUIC-пакеты могут обходить маршрутизацию или утекать через физический интерфейс.
Блокировка UDP 443 заставляет браузеры переключиться на TCP-based HTTP/2 —
весь трафик гарантированно идёт через TUN туннель.

В `internal/config/singbox_builder.go`, `buildRoute`, добавить правило **первым**
(до Telegram CIDR, чтобы перехватить до address-based правил):

```go
// QUIC/HTTP3 блокировка: force TCP-based HTTP/2 через туннель
{
    Network: "udp",
    Port:    []uint16{443},
    Action:  "reject",
},
```

Добавить настройку в `AppSettings` (или `RoutingConfig`) для включения/отключения:
```go
BlockQUIC bool `json:"block_quic"` // default: true
```

В `buildRoute`:
```go
if routingCfg.BlockQUIC {
    rules = append([]SBRule{{Network: "udp", Port: []uint16{443}, Action: "reject"}}, rules...)
}
```

---

## ГРУППА В — Надёжность

### В1. Автофейловер при деградации соединения

**Проблема:** `HealthChecker.RecordError()` возвращает `true` при ≥5 ошибок за 30с,
но никакого действия не происходит. `handleAutoConnect` уже полностью реализован.

**В1.1** В `internal/xray/manager.go`, `Manager` interface (стр.142):
```go
// SetHealthAlertFn регистрирует callback — вызывается в горутине при деградации.
SetHealthAlertFn(fn func())
```

В `manager` struct (стр.269) добавить:
```go
healthAlertFn  func()
healthAlertMu  sync.Mutex
```

Реализация:
```go
func (m *manager) SetHealthAlertFn(fn func()) {
    m.healthAlertMu.Lock()
    m.healthAlertFn = fn
    m.healthAlertMu.Unlock()
}
```

**В1.2** В `internal/xray/health_tracking_writer.go`, место вызова
`h.checker.RecordError(...)`:
```go
if triggered := h.checker.RecordError(time.Now(), errorType, outbound, lineStr); triggered {
    m := h.manager // *manager, передаётся при создании
    if m != nil {
        m.healthAlertMu.Lock()
        fn := m.healthAlertFn
        m.healthAlertMu.Unlock()
        if fn != nil {
            go fn()
        }
    }
}
```

Обновить `NewHealthTrackingWriter` чтобы принимал `*manager`.

**В1.3** В `internal/api/servers_handlers.go` добавить публичный метод:
```go
// AutoConnect — программный вызов автоподключения (без HTTP контекста).
// Используется как callback при деградации соединения.
func (h *ServersHandlers) AutoConnect() {
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    h.doAutoConnect(ctx) // логика handleAutoConnect без http.ResponseWriter
}
```

Рефакторить `handleAutoConnect`: вынести основную логику в `doAutoConnect(ctx)`,
`handleAutoConnect` вызывает её и пишет результат в `w`.

**В1.4** В `cmd/proxy-client/app.go`, `finalizeStartup`:
```go
if sh := a.serversHandlers; sh != nil {
    a.xrayManager.SetHealthAlertFn(func() {
        a.logger.Info("HealthAlert: деградация — запуск автоподключения")
        sh.AutoConnect()
    })
}
```

---

### В2. Периодический тихий реконнект

**Проблема:** долго живущие соединения (>2ч) идентифицируются DPI как VPN-сессии.
Периодический реконнект через `TriggerApply` не разрывает трафик (hot reload).

**В2.1** В `internal/config/tun.go` (или `AppSettings`):
```go
ReconnectIntervalMin int `json:"reconnect_interval_min"` // 0 = отключено
```

**В2.2** В `internal/api/server.go` добавить в `Server` struct:
```go
reconnectStop chan struct{}
```

Метод:
```go
func (s *Server) StartPeriodicReconnect(interval time.Duration) {
    if interval <= 0 {
        return
    }
    s.reconnectStop = make(chan struct{})
    go func() {
        t := time.NewTicker(interval)
        defer t.Stop()
        for {
            select {
            case <-t.C:
                if s.tunHandlers != nil {
                    s.logger.Debug("PeriodicReconnect: ротация сессии")
                    _ = s.tunHandlers.TriggerApply()
                }
            case <-s.reconnectStop:
                return
            case <-s.lifecycleCtx.Done():
                return
            }
        }
    }()
}
```

**В2.3** В `app.go`, `finalizeStartup`:
```go
if settings.ReconnectIntervalMin > 0 {
    a.apiServer.StartPeriodicReconnect(
        time.Duration(settings.ReconnectIntervalMin) * time.Minute,
    )
}
```

---

### В3. Детекция смены сети + авто-реконнект

**Проблема:** при переключении WiFi → Ethernet (или смене сети) sing-box зависает
на старом интерфейсе — трафик перестаёт идти без видимой ошибки.

**В3.1** Создать `internal/netwatch/netwatch_windows.go`:
```go
//go:build windows

package netwatch

import (
    "context"
    "syscall"
    "unsafe"
    "time"
    "golang.org/x/sys/windows"
)

var (
    iphlpapi                  = windows.NewLazySystemDLL("iphlpapi.dll")
    procNotifyIpInterfaceChange = iphlpapi.NewProc("NotifyIpInterfaceChange")
    procCancelMibChangeNotify2  = iphlpapi.NewProc("CancelMibChangeNotify2")
)

// Watch вызывает onChange при каждом изменении сетевого интерфейса.
// Блокируется до отмены ctx.
func Watch(ctx context.Context, onChange func()) error {
    var handle uintptr
    // AF_UNSPEC=0: следим за IPv4 и IPv6 интерфейсами
    cb := syscall.NewCallback(func(callerCtx, row, notificationType uintptr) uintptr {
        go onChange()
        return 0
    })
    r, _, err := procNotifyIpInterfaceChange.Call(0, cb, 0, 1,
        uintptr(unsafe.Pointer(&handle)))
    if r != 0 {
        return fmt.Errorf("NotifyIpInterfaceChange: %w", err)
    }
    <-ctx.Done()
    procCancelMibChangeNotify2.Call(handle)
    return nil
}
```

Создать `internal/netwatch/netwatch_other.go`:
```go
//go:build !windows

package netwatch

import "context"

func Watch(ctx context.Context, onChange func()) error {
    <-ctx.Done()
    return nil
}
```

**В3.2** В `cmd/proxy-client/app.go`, `finalizeStartup`:
```go
go func() {
    netwatch.Watch(a.lifecycleCtx, func() {
        // Дебаунс: не реконнектиться чаще раза в 3 секунды
        time.Sleep(3 * time.Second)
        a.logger.Info("Netwatch: смена сети — переприменяем конфиг")
        if a.apiServer.tunHandlers != nil {
            _ = a.apiServer.tunHandlers.TriggerApply()
        }
    })
}()
```

---

### В4. Детекция конфликтов портов при старте

**Проблема:** если порты 10807 или 9090 заняты — sing-box падает с непонятным FATAL.
Уже есть `handlePortStatus` в `diag_handlers.go` — расширить до pre-flight проверки.

В `cmd/proxy-client/app.go`, в начале `startBackground`, перед `NewManager`:
```go
busyPorts := checkPortsBusy([]int{config.ProxyPort, 9090})
if len(busyPorts) > 0 {
    a.logger.Error("Порты заняты другим процессом: %v — sing-box не запустится", busyPorts)
    notification.Show("SafeSky", fmt.Sprintf(
        "Порт %d занят. Завершите конфликтующий процесс.", busyPorts[0],
    ))
}

// checkPortsBusy — в том же файле или в internal/portcheck/
func checkPortsBusy(ports []int) []int {
    var busy []int
    for _, p := range ports {
        ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
        if err != nil {
            busy = append(busy, p)
            continue
        }
        ln.Close()
    }
    return busy
}
```

---

### В5. Автоисключение Windows Defender

**Проблема:** sing-box при первом запуске блокируется SmartScreen → `0xc0000005`.
Уже есть `isAccessViolation` детектор — добавить auto-fix.

В `internal/engine/engine.go` или в `cmd/proxy-client/app.go`,
в месте обработки `isAccessViolation(err) == true`:

```go
func addDefenderExclusion(path string) {
    absPath, err := filepath.Abs(path)
    if err != nil {
        return
    }
    cmd := exec.Command("powershell", "-NonInteractive", "-Command",
        fmt.Sprintf("Add-MpPreference -ExclusionPath '%s'", absPath))
    cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
    if err := cmd.Run(); err != nil {
        return
    }
}

// В месте обработки AV-краша:
if isAccessViolation(startErr) {
    a.logger.Warn("Windows Defender блокирует sing-box — добавляем исключение")
    addDefenderExclusion(a.config.SingBoxPath)
    time.Sleep(2 * time.Second)
}
```

---

### В6. Анти-idle keepalive

**Проблема:** ISP и промежуточные маршрутизаторы сбрасывают idle TCP-соединения
через 5–10 минут бездействия. VPN-соединение "зависает" незаметно для пользователя
— ping есть, но трафик не идёт.

**В6.1** В `internal/config/tun.go` добавить в `AppSettings`:
```go
KeepaliveEnabled     bool `json:"keepalive_enabled"`      // default: true
KeepaliveIntervalSec int  `json:"keepalive_interval_sec"` // default: 120
```

**В6.2** Создать `internal/keepalive/keepalive.go`:
```go
package keepalive

import (
    "context"
    "net/http"
    "time"
)

// Run периодически отправляет HEAD-запрос через прокси чтобы не дать
// ISP-у закрыть idle TCP-соединение до VPN сервера.
// Использует HTTP прокси (не TUN), чтобы трафик точно шёл через VLESS.
func Run(ctx context.Context, proxyAddr string, interval time.Duration) {
    if interval <= 0 {
        interval = 120 * time.Second
    }
    t := time.NewTicker(interval)
    defer t.Stop()
    client := buildProxyClient(proxyAddr)
    for {
        select {
        case <-ctx.Done():
            return
        case <-t.C:
            req, _ := http.NewRequestWithContext(ctx, "HEAD",
                "http://connectivitycheck.gstatic.com/generate_204", nil)
            resp, err := client.Do(req)
            if err == nil {
                resp.Body.Close()
            }
        }
    }
}

func buildProxyClient(addr string) *http.Client {
    // addr = "127.0.0.1:10807"
    t := &http.Transport{}
    if addr != "" {
        t.Proxy = http.ProxyURL(mustParseURL("http://" + addr))
    }
    return &http.Client{Transport: t, Timeout: 10 * time.Second}
}
```

**В6.3** В `cmd/proxy-client/app.go`, `finalizeStartup`:
```go
if settings.KeepaliveEnabled {
    interval := time.Duration(settings.KeepaliveIntervalSec) * time.Second
    if interval == 0 {
        interval = 120 * time.Second
    }
    go keepalive.Run(a.lifecycleCtx,
        fmt.Sprintf("127.0.0.1:%d", config.ProxyPort), interval)
}
```

---

## ГРУППА Г — Новые функции

### Г1. LAN Sharing — раздача VPN по локальной сети

**Проблема:** другие устройства в LAN не могут использовать VPN через этот компьютер.

**Г1.1** В `internal/config/tun.go`, `AppSettings` добавить:
```go
LANShareEnabled bool `json:"lan_share_enabled"`
LANSharePort    int  `json:"lan_share_port"` // default 10808
```

**Г1.2** В `internal/config/singbox_builder.go`, `buildSingBoxConfig`,
в блок `Inbounds` если `routingCfg.LANShare` включён:
```go
if routingCfg.LANShareEnabled {
    lanPort := routingCfg.LANSharePort
    if lanPort == 0 {
        lanPort = 10808
    }
    cfg.Inbounds = append(cfg.Inbounds, SBInbound{
        Type:       "http",
        Tag:        "http-lan",
        Listen:     "0.0.0.0",
        ListenPort: lanPort,
    })
}
```

**Г1.3** Добавить API-эндпоинт `GET /api/settings/lan-info` возвращающий
локальные IP-адреса машины для отображения пользователю.

---

### Г2. Speed Test

**Проблема:** пользователи не знают реальную скорость VPN-соединения.

**Г2.1** Создать `internal/speedtest/speedtest.go`:
```go
package speedtest

import (
    "context"
    "io"
    "net/http"
    "time"
)

type Result struct {
    DownloadMbps float64
    LatencyMs    int64
    Error        string
}

// Run скачивает тестовый файл через указанный прокси и измеряет скорость.
func Run(ctx context.Context, proxyAddr string) Result {
    start := time.Now()
    testURL := "https://speed.cloudflare.com/__down?bytes=10000000"

    transport := &http.Transport{}
    if proxyAddr != "" {
        transport.Proxy = http.ProxyURL(mustParseURL("http://" + proxyAddr))
    }
    client := &http.Client{Transport: transport, Timeout: 30 * time.Second}

    req, _ := http.NewRequestWithContext(ctx, "GET", testURL, nil)
    resp, err := client.Do(req)
    if err != nil {
        return Result{Error: err.Error()}
    }
    defer resp.Body.Close()

    latency := time.Since(start).Milliseconds()
    n, _ := io.Copy(io.Discard, resp.Body)
    elapsed := time.Since(start).Seconds()
    mbps := float64(n) / elapsed / 125000 // bytes → Mbps

    return Result{DownloadMbps: mbps, LatencyMs: latency}
}
```

**Г2.2** Добавить в `SetupFeatureRoutes`:
```go
api.HandleFunc("/speedtest", s.handleSpeedTest).Methods("POST", "OPTIONS")
```

---

### Г3. DNS Leak Test + IP Leak Test

**Проблема:** пользователи хотят убедиться что реальный IP не утекает.

Добавить `GET /api/leak-check` в `SetupFeatureRoutes`:

```go
func (s *Server) handleLeakCheck(w http.ResponseWriter, r *http.Request) {
    ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
    defer cancel()

    proxyURL, _ := url.Parse("http://127.0.0.1:" + strconv.Itoa(config.ProxyPort))
    transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
    client := &http.Client{Transport: transport, Timeout: 8 * time.Second}

    proxyIP := fetchIP(ctx, client, "https://api.ipify.org?format=text")
    directIP := fetchIP(ctx, http.DefaultClient, "https://api.ipify.org?format=text")

    s.respondJSON(w, http.StatusOK, map[string]interface{}{
        "direct_ip": directIP,
        "proxy_ip":  proxyIP,
        "leaked":    directIP != "" && proxyIP != "" && directIP == proxyIP,
    })
}
```

---

### Г4. QR-код для VLESS URL

**Проблема:** передать сервер на мобильное устройство неудобно.

Добавить зависимость `github.com/skip2/go-qrcode` (pure Go, нет CGO).

```go
// GET /api/servers/{id}/qr — возвращает PNG QR-кода
func (h *ServersHandlers) handleQR(w http.ResponseWriter, r *http.Request) {
    id := mux.Vars(r)["id"]
    // ... найти сервер по id ...
    png, err := qrcode.Encode(srv.URL, qrcode.Medium, 256)
    if err != nil {
        h.server.respondError(w, http.StatusInternalServerError, err.Error())
        return
    }
    w.Header().Set("Content-Type", "image/png")
    w.Write(png)
}
```

Зарегистрировать: `api.HandleFunc("/servers/{id}/qr", h.handleQR).Methods("GET", "OPTIONS")`

---

### Г5. Импорт правил из внешних форматов

**Проблема:** пользователи имеют списки доменов/IP из Clash, v2rayN, простых текстовых файлов.

Добавить `POST /api/tun/rules/import`:

```go
// Body: { "format": "text|clash|gfwlist", "content": "...", "action": "proxy|direct|block" }
// "text"    — один домен/IP/CIDR в строке
// "clash"   — "DOMAIN,example.com,PROXY" или "IP-CIDR,1.2.3.0/24,DIRECT"
// "gfwlist" — base64-encoded PAC список
func (h *TunHandlers) handleImportRules(w http.ResponseWriter, r *http.Request) { ... }
```

Парсер возвращает `[]config.RoutingRule`, которые мержатся с существующими
через `handleBulkReplaceRules` логику.

---

### Г6. Шаблоны правил (Preset Profiles)

**Проблема:** новые пользователи не знают какие правила настроить.

Встроить 3 готовых профиля как JSON-константы в `profile_handlers.go`:

```go
var builtinProfiles = map[string]string{
    "bypass-ru": `{"name":"Только заблокированное","routing":{"default_action":"direct","rules":[
        {"value":"geosite:ru-blocked","type":"geosite","action":"proxy"},
        {"value":"geosite:category-ads-all","type":"geosite","action":"block"}
    ]}}`,
    "proxy-all": `{"name":"Всё через прокси","routing":{"default_action":"proxy","rules":[
        {"value":"geosite:ru","type":"geosite","action":"direct"},
        {"value":"geosite:private","type":"geosite","action":"direct"}
    ]}}`,
    "work": `{"name":"Только рабочие домены","routing":{"default_action":"direct","rules":[]}}`,
}
```

Добавить `GET /api/profiles/builtins` возвращающий список встроенных шаблонов.

---

### Г7. Планировщик (расписание прокси)

**Проблема:** нужно автоматически включать/выключать прокси в рабочие часы.

**Г7.1** В `internal/config/tun.go` добавить:
```go
type Schedule struct {
    Enabled  bool   `json:"enabled"`
    ProxyOn  string `json:"proxy_on"`  // "09:00"
    ProxyOff string `json:"proxy_off"` // "18:00"
    Weekdays []int  `json:"weekdays"`  // 1=пн, 7=вс; пусто = каждый день
}
```

**Г7.2** В `cmd/proxy-client/app.go`, `finalizeStartup`:
```go
if settings.Schedule.Enabled {
    go runScheduler(a.lifecycleCtx, settings.Schedule, a.apiServer)
}

func runScheduler(ctx context.Context, s config.Schedule, srv *api.Server) {
    for {
        select {
        case <-ctx.Done():
            return
        case <-time.After(30 * time.Second):
            now := time.Now()
            shouldBeOn := isWithinSchedule(now, s)
            proxyOn := srv.IsProxyEnabled()
            if shouldBeOn && !proxyOn {
                srv.EnableProxy()
            } else if !shouldBeOn && proxyOn {
                srv.DisableProxy()
            }
        }
    }
}
```

---

### Г8. Автоимпорт VLESS из буфера обмена

**Проблема:** пользователи копируют VLESS URL из Telegram/браузера, затем вручную
открывают "Добавить сервер" и вставляют — лишние шаги.

**Г8.1** В `internal/api/server.go` добавить хендлер:
```go
// GET /api/clipboard/vless — возвращает VLESS URL из буфера обмена если есть
func (s *Server) handleClipboardVLESS(w http.ResponseWriter, r *http.Request) {
    text := readClipboard() // см. Г8.2
    if !strings.HasPrefix(text, "vless://") {
        s.respondJSON(w, http.StatusOK, map[string]interface{}{"found": false})
        return
    }
    // Парсим чтобы убедиться что URL валиден
    if _, err := config.ParseVLESSContent(text); err != nil {
        s.respondJSON(w, http.StatusOK, map[string]interface{}{"found": false})
        return
    }
    s.respondJSON(w, http.StatusOK, map[string]interface{}{
        "found": true,
        "url":   text,
    })
}
```

**Г8.2** Создать `internal/clipboard/clipboard_windows.go`:
```go
//go:build windows

package clipboard

import (
    "golang.org/x/sys/windows"
    "unsafe"
    "runtime"
)

var (
    user32          = windows.NewLazySystemDLL("user32.dll")
    procOpenClipboard  = user32.NewProc("OpenClipboard")
    procCloseClipboard = user32.NewProc("CloseClipboard")
    procGetClipboard   = user32.NewProc("GetClipboardData")
    kernel32        = windows.NewLazySystemDLL("kernel32.dll")
    procGlobalLock  = kernel32.NewProc("GlobalLock")
    procGlobalUnlock = kernel32.NewProc("GlobalUnlock")
)

const CF_UNICODETEXT = 13

func Read() string {
    procOpenClipboard.Call(0)
    defer procCloseClipboard.Call()
    h, _, _ := procGetClipboard.Call(CF_UNICODETEXT)
    if h == 0 {
        return ""
    }
    ptr, _, _ := procGlobalLock.Call(h)
    defer procGlobalUnlock.Call(h)
    if ptr == 0 {
        return ""
    }
    // UTF-16LE → Go string
    n := 0
    for p := ptr; *(*uint16)(unsafe.Pointer(p + uintptr(n)*2)) != 0; n++ {}
    runtime.KeepAlive(ptr)
    return windows.UTF16PtrToString((*uint16)(unsafe.Pointer(ptr)))
}
```

Создать `internal/clipboard/clipboard_other.go`:
```go
//go:build !windows

package clipboard

func Read() string { return "" }
```

**Г8.3** Зарегистрировать в `SetupFeatureRoutes`:
```go
api.HandleFunc("/clipboard/vless", s.handleClipboardVLESS).Methods("GET", "OPTIONS")
```

В UI: при фокусе окна вызывать `GET /api/clipboard/vless` и если `found: true` — 
показывать баннер "Обнаружен VLESS в буфере обмена — добавить сервер?".

---

### Г9. Экспорт/Импорт полной конфигурации (ZIP)

**Проблема:** при переустановке или смене ПК нужно вручную пересохранять все серверы,
правила и профили. Нет единого способа сделать резервную копию.

**Г9.1** Добавить `GET /api/backup/export` — создаёт ZIP-архив в памяти:
```go
func (s *Server) handleExportConfig(w http.ResponseWriter, r *http.Request) {
    buf := &bytes.Buffer{}
    zw := zip.NewWriter(buf)

    // Включить: servers.json, settings.json, profiles/*.json, rules/*.json
    filesToInclude := []string{
        "data/servers.json",
        "data/settings.json",
    }
    // Добавить все профили
    profileFiles, _ := filepath.Glob("data/profiles/*.json")
    filesToInclude = append(filesToInclude, profileFiles...)

    for _, path := range filesToInclude {
        data, err := os.ReadFile(path)
        if err != nil {
            continue
        }
        f, _ := zw.Create(filepath.Base(path))
        f.Write(data)
    }
    zw.Close()

    w.Header().Set("Content-Type", "application/zip")
    w.Header().Set("Content-Disposition",
        fmt.Sprintf("attachment; filename=safesky-backup-%s.zip",
            time.Now().Format("2006-01-02")))
    w.Write(buf.Bytes())
}
```

**Г9.2** Добавить `POST /api/backup/import` — принимает ZIP и восстанавливает файлы:
```go
// Multipart upload: file=<zip>
// Валидирует структуру ZIP перед применением (не перезаписывает secret.key)
func (s *Server) handleImportConfig(w http.ResponseWriter, r *http.Request) { ... }
```

Зарегистрировать:
```go
api.HandleFunc("/backup/export", s.handleExportConfig).Methods("GET", "OPTIONS")
api.HandleFunc("/backup/import", s.handleImportConfig).Methods("POST", "OPTIONS")
```

---

## ГРУППА Д — Мониторинг и диагностика

### Д1. Трафик по процессам

**Проблема:** Clash API `/connections` уже возвращает данные с именем процесса и
`downloadTotal/uploadTotal` — но нет агрегации по процессам.

В `internal/api/diag_handlers.go` добавить хендлер:

```go
// GET /api/traffic/by-process — агрегированный трафик по процессам
func (h *DiagHandlers) handleTrafficByProcess(w http.ResponseWriter, r *http.Request) {
    conns, err := h.conns.fetchConnectionsData(r.Context())
    if err != nil {
        respondError(w, http.StatusServiceUnavailable, err.Error())
        return
    }
    type procStat struct {
        Process  string `json:"process"`
        Download int64  `json:"download"`
        Upload   int64  `json:"upload"`
        Conns    int    `json:"connections"`
    }
    agg := map[string]*procStat{}
    for _, c := range conns {
        proc := filepath.Base(c.Metadata.ProcessPath)
        if proc == "" || proc == "." {
            proc = "unknown"
        }
        if _, ok := agg[proc]; !ok {
            agg[proc] = &procStat{Process: proc}
        }
        agg[proc].Download += c.Download
        agg[proc].Upload += c.Upload
        agg[proc].Conns++
    }
    // Собрать в slice, отсортировать по Download desc
}
```

Зарегистрировать через `SetupFeatureRoutes`.
Добавить в silent paths: `s.addSilentPath("/api/traffic/by-process")`.

---

### Д2. История латентности серверов

**Проблема:** нет возможности увидеть тренд: ухудшается ли соединение со временем.

Создать `internal/latency/tracker.go`:

```go
package latency

import "sync"

const maxPoints = 120 // 60 минут при пинге раз в 30с

type Point struct {
    TS    int64 `json:"ts"`    // unix
    Ms    int64 `json:"ms"`    // -1 если таймаут
}

type Tracker struct {
    mu      sync.RWMutex
    history map[string][]Point // serverID → []Point
}

var Global = &Tracker{history: make(map[string][]Point)}

func (t *Tracker) Record(serverID string, ms int64) {
    t.mu.Lock()
    defer t.mu.Unlock()
    pts := append(t.history[serverID], Point{TS: time.Now().Unix(), Ms: ms})
    if len(pts) > maxPoints {
        pts = pts[len(pts)-maxPoints:]
    }
    t.history[serverID] = pts
}

func (t *Tracker) Get(serverID string) []Point {
    t.mu.RLock()
    defer t.mu.RUnlock()
    return append([]Point{}, t.history[serverID]...)
}
```

Добавить `GET /api/servers/{id}/latency-history`.
Фоновый пингер в `app.go` — раз в 30с пинговать все серверы и вызывать
`latency.Global.Record(id, ms)`.

---

### Д3. Цвет иконки трея по состоянию здоровья

**Проблема:** иконка всегда одного цвета — пользователь не знает о деградации.

В `internal/tray/tray.go` добавить:
```go
type HealthState int
const (
    HealthOK       HealthState = iota // зелёная иконка
    HealthDegraded                    // жёлтая
    HealthCritical                    // красная (≥ порога HealthChecker)
)

func SetHealthState(state HealthState) {
    win32SetIconByHealth(state) // 3 варианта иконки в icon.go
}
```

В `icon.go` хранить 3 версии иконки (OK/Degraded/Critical).

В фоновой горутине `app.go` — раз в 10с читать `xrayMgr.GetHealthStatus()`:
```go
go func() {
    t := time.NewTicker(10 * time.Second)
    defer t.Stop()
    for {
        select {
        case <-t.C:
            cnt, _, wouldAlert := a.xrayManager.GetHealthStatus()
            switch {
            case wouldAlert:
                tray.SetHealthState(tray.HealthCritical)
            case cnt > 0:
                tray.SetHealthState(tray.HealthDegraded)
            default:
                tray.SetHealthState(tray.HealthOK)
            }
        case <-a.lifecycleCtx.Done():
            return
        }
    }
}()
```

---

### Д4. Счётчик трафика (сессия + суммарно)

**Проблема:** нет статистики сколько данных прошло через VPN.

Создать `internal/trafficstats/stats.go`:
```go
package trafficstats

import (
    "encoding/json"
    "sync/atomic"
    "proxyclient/internal/fileutil"
)

const statsFile = "data/traffic_stats.json"

type Stats struct {
    TotalDownloadBytes int64 `json:"total_download_bytes"`
    TotalUploadBytes   int64 `json:"total_upload_bytes"`
    TotalSessions      int64 `json:"total_sessions"`
}

var (
    sessionDown atomic.Int64
    sessionUp   atomic.Int64
)

func AddSession(down, up int64) {
    sessionDown.Add(down)
    sessionUp.Add(up)
}

func SaveToFile(sessionDownBytes, sessionUpBytes int64) {
    s := load()
    s.TotalDownloadBytes += sessionDownBytes
    s.TotalUploadBytes += sessionUpBytes
    s.TotalSessions++
    data, _ := json.MarshalIndent(s, "", "  ")
    fileutil.WriteAtomic(statsFile, data, 0644)
}

func load() Stats {
    data, err := os.ReadFile(statsFile)
    if err != nil {
        return Stats{}
    }
    var s Stats
    json.Unmarshal(data, &s)
    return s
}
```

В `app.go`, `Shutdown()` — вызвать `trafficstats.SaveToFile(sessionDown, sessionUp)`.
Добавить `GET /api/stats/total` возвращающий накопленные + сессионные данные.

---

### Д5. Умный краш-отчёт

**Проблема:** при краше sing-box пользователь не знает что случилось и что передать в поддержку.

```go
// internal/crashreport/report.go
type CrashReport struct {
    Timestamp   string `json:"timestamp"`
    SingBoxVer  string `json:"singbox_version"`
    AppVer      string `json:"app_version"`
    WindowsVer  string `json:"windows_version"`
    LastOutput  string `json:"last_output"`  // последние 100 строк stderr
    ConfigSafe  string `json:"config_safe"`  // конфиг без секретов (UUID маскирован)
    MemoryMB    uint64 `json:"memory_mb"`
    ErrorMsg    string `json:"error_message"`
}

func Generate(output, errMsg, configPath string) *CrashReport { ... }
func (r *CrashReport) SaveToFile() string { ... }
```

Сохранять в `data/crash-YYYY-MM-DD-HHMMSS.json`.
Добавить `GET /api/diagnostics/crashes` — список последних 5 краш-отчётов.
UUID в `config_safe` маскировать: `****-****`.

---

### Д6. История событий подключения

**Проблема:** нет журнала connect/disconnect событий — непонятно когда и почему
произошёл автофейловер или реконнект.

Создать `internal/connhistory/history.go`:
```go
package connhistory

import (
    "sync"
    "time"
)

type EventKind string
const (
    EventConnect    EventKind = "connect"
    EventDisconnect EventKind = "disconnect"
    EventFailover   EventKind = "failover"
    EventReconnect  EventKind = "reconnect"
    EventNetChange  EventKind = "net_change"
)

type Event struct {
    Time     time.Time `json:"time"`
    Kind     EventKind `json:"kind"`
    Server   string    `json:"server,omitempty"`
    LatencyMs int64    `json:"latency_ms,omitempty"`
    Reason   string    `json:"reason,omitempty"`
}

const maxEvents = 100

type History struct {
    mu     sync.RWMutex
    events []Event
}

var Global = &History{}

func (h *History) Add(e Event) {
    h.mu.Lock()
    defer h.mu.Unlock()
    h.events = append(h.events, e)
    if len(h.events) > maxEvents {
        h.events = h.events[len(h.events)-maxEvents:]
    }
}

func (h *History) All() []Event {
    h.mu.RLock()
    defer h.mu.RUnlock()
    result := make([]Event, len(h.events))
    copy(result, h.events)
    return result
}
```

В местах connect/disconnect/failover/reconnect добавить вызовы:
```go
connhistory.Global.Add(connhistory.Event{
    Time:   time.Now(),
    Kind:   connhistory.EventConnect,
    Server: serverName,
    LatencyMs: latency,
})
```

Добавить `GET /api/connections/history` в `SetupFeatureRoutes`.

---

### Д7. Авто-перезапуск при утечке памяти sing-box

**Проблема:** sing-box при длительной работе (>24ч) накапливает утечки памяти
в некоторых конфигурациях. Нет механизма автоматического "оздоровления".

**Д7.1** В `internal/xray/manager.go`, `Manager` interface добавить:
```go
// MemoryMB возвращает RSS sing-box процесса в МБ. 0 если не запущен.
MemoryMB() uint64
```

**Д7.2** Реализация через `GetProcessMemoryInfo` из `psapi.dll`:
```go
//go:build windows

func (m *manager) MemoryMB() uint64 {
    pid := m.GetPID()
    if pid == 0 {
        return 0
    }
    h, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ,
        false, uint32(pid))
    if err != nil {
        return 0
    }
    defer windows.CloseHandle(h)
    var mc windows.PROCESS_MEMORY_COUNTERS
    if err := windows.GetProcessMemoryInfo(h, &mc, uint32(unsafe.Sizeof(mc))); err != nil {
        return 0
    }
    return uint64(mc.WorkingSetSize) / 1024 / 1024
}
```

**Д7.3** В `AppConfig` добавить:
```go
MemoryLimitMB uint64 `json:"memory_limit_mb"` // 0 = отключено; recommended: 512
```

**Д7.4** В фоновой горутине `app.go` — раз в 60с:
```go
go func() {
    t := time.NewTicker(60 * time.Second)
    defer t.Stop()
    for {
        select {
        case <-t.C:
            if a.config.MemoryLimitMB == 0 {
                continue
            }
            used := a.xrayManager.MemoryMB()
            if used > a.config.MemoryLimitMB {
                a.logger.Warn("sing-box: %d MB > лимит %d MB — мягкий перезапуск",
                    used, a.config.MemoryLimitMB)
                connhistory.Global.Add(connhistory.Event{
                    Kind:   connhistory.EventReconnect,
                    Reason: fmt.Sprintf("memory limit %dMB", used),
                })
                _ = a.apiServer.tunHandlers.TriggerApply()
            }
        case <-a.lifecycleCtx.Done():
            return
        }
    }
}()
```

---

### Д8. Блокировка телеметрии Windows

**Проблема:** Windows отправляет телеметрию даже в частный браузер, что может
использоваться для корреляции трафика. Блокировка через sing-box routing — без hosts-file.

В `internal/config/singbox_builder.go`, добавить константу:
```go
// windowsTelemetryDomains — домены телеметрии Microsoft для блокировки.
var windowsTelemetryDomains = []string{
    "telemetry.microsoft.com",
    "vortex.data.microsoft.com",
    "settings-win.data.microsoft.com",
    "watson.telemetry.microsoft.com",
    "oca.telemetry.microsoft.com",
    "sqm.telemetry.microsoft.com",
    "v10.events.data.microsoft.com",
    "v20.events.data.microsoft.com",
    "self.events.data.microsoft.com",
    "pipe.aria.microsoft.com",
    "browser.pipe.aria.microsoft.com",
    "telecommand.telemetry.microsoft.com",
}
```

В `buildRoute`, при `settings.BlockTelemetry`:
```go
if settings.BlockTelemetry {
    rules = append(rules, SBRule{
        Domain: windowsTelemetryDomains,
        Action: "reject",
    })
}
```

В `AppSettings`:
```go
BlockTelemetry bool `json:"block_telemetry"` // default: false (opt-in)
```

---

## Порядок реализации

### Фаза 1 — Быстрые победы (1–2 часа каждое, максимальный эффект)

| Приоритет | Задача | Файлы |
|-----------|--------|-------|
| 🔴 1 | А1 Clash API Secret | `singbox_builder.go`, `server.go`, `diag_handlers.go` |
| 🔴 2 | А3 Multiplex Padding | `singbox_builder.go` |
| 🔴 3 | Б1 TLS Fragmentation | `singbox_types.go`, `vless.go`, `singbox_builder.go` |
| 🔴 4 | Б3 STUN blocking | `singbox_builder.go` |
| 🔴 5 | Б5 QUIC blocking | `singbox_builder.go` |
| 🟠 6 | А4 TLS MinVersion | `singbox_types.go`, `singbox_builder.go` |
| 🟠 7 | Б2 ALPN + FP selector | `singbox_types.go`, `vless.go`, `singbox_builder.go` |
| 🟠 8 | В4 Port conflict check | `app.go` |
| 🟠 9 | В5 Defender exclusion | `engine.go` или `app.go` |
| 🟡 10 | Г3 Leak Test | новый хендлер |
| 🟡 11 | Д3 Tray health color | `tray.go`, `icon.go`, `app.go` |
| 🟡 12 | Д8 Telemetry blocking | `singbox_builder.go` |

### Фаза 2 — Средней сложности (полдня каждое)

| Приоритет | Задача | Файлы |
|-----------|--------|-------|
| 13 | А2 DPAPI | NEW `internal/dpapi/`, `vless.go`, `servers_handlers.go` |
| 14 | Б4 DNS Fallback | `tun.go`, `singbox_builder.go` |
| 15 | В1 Autofailover | `manager.go`, `health_tracking_writer.go`, `servers_handlers.go`, `app.go` |
| 16 | В2 Periodic reconnect | `server.go`, `app.go` |
| 17 | В3 Network change | NEW `internal/netwatch/`, `app.go` |
| 18 | В6 Keepalive | NEW `internal/keepalive/`, `app.go` |
| 19 | Г1 LAN sharing | `singbox_builder.go`, `settings_handlers.go` |
| 20 | Г4 QR code | `servers_handlers.go` (+зависимость `skip2/go-qrcode`) |
| 21 | Г6 Rule templates | `profile_handlers.go` |
| 22 | Г8 Clipboard import | NEW `internal/clipboard/`, хендлер |
| 23 | Д1 Traffic by process | `diag_handlers.go` |
| 24 | Д4 Traffic counter | NEW `internal/trafficstats/`, `app.go` |
| 25 | Д6 Connection history | NEW `internal/connhistory/`, `app.go` |

### Фаза 3 — Крупные фичи (день и более)

| Приоритет | Задача | Файлы |
|-----------|--------|-------|
| 26 | Г2 Speed Test | NEW `internal/speedtest/`, хендлер |
| 27 | Г5 Rule import | `tun_handlers.go` |
| 28 | Г7 Scheduler | `tun.go`, `app.go` |
| 29 | Г9 Config backup/import | NEW endpoint пара |
| 30 | Д2 Latency history | NEW `internal/latency/`, `servers_handlers.go`, `app.go` |
| 31 | Д5 Crash report | NEW `internal/crashreport/`, `app.go` |
| 32 | Д7 Memory watchdog | `manager.go`, `app.go` |

---

## Итоговая таблица всех изменений

| # | Название | Группа | Новые файлы | Изменяемые файлы |
|---|----------|--------|-------------|-----------------|
| А1 | Clash API Secret | Безопасность | — | `singbox_builder.go`, `server.go`, `diag_handlers.go` |
| А2 | DPAPI secret.key | Безопасность | `internal/dpapi/*` | `vless.go`, `servers_handlers.go` |
| А3 | Multiplex Padding | Безопасность | — | `singbox_builder.go` |
| А4 | TLS MinVersion | Безопасность | — | `singbox_types.go`, `singbox_builder.go` |
| Б1 | TLS Fragmentation | Anti-DPI | — | `singbox_types.go`, `vless.go`, `singbox_builder.go` |
| Б2 | ALPN + FP selector | Anti-DPI | — | `singbox_types.go`, `vless.go`, `singbox_builder.go` |
| Б3 | STUN blocking | Anti-DPI | — | `singbox_builder.go` |
| Б4 | DNS Fallback | Anti-DPI | — | `tun.go`, `singbox_builder.go` |
| Б5 | QUIC/HTTP3 blocking | Anti-DPI | — | `singbox_builder.go` |
| В1 | Autofailover | Надёжность | — | `manager.go`, `health_tracking_writer.go`, `servers_handlers.go`, `app.go` |
| В2 | Periodic reconnect | Надёжность | — | `server.go`, `app.go` |
| В3 | Netwatch | Надёжность | `internal/netwatch/*` | `app.go` |
| В4 | Port conflict | Надёжность | — | `app.go` |
| В5 | Defender exclusion | Надёжность | — | `engine.go` |
| В6 | Keepalive | Надёжность | `internal/keepalive/` | `app.go` |
| Г1 | LAN sharing | Функции | — | `singbox_builder.go`, `settings_handlers.go` |
| Г2 | Speed Test | Функции | `internal/speedtest/*` | хендлер в `server.go` |
| Г3 | Leak Test | Функции | — | новый хендлер |
| Г4 | QR код | Функции | — | `servers_handlers.go` |
| Г5 | Rule import | Функции | — | `tun_handlers.go` |
| Г6 | Rule templates | Функции | — | `profile_handlers.go` |
| Г7 | Scheduler | Функции | — | `tun.go`, `app.go` |
| Г8 | Clipboard import | Функции | `internal/clipboard/*` | `server.go` |
| Г9 | Config backup ZIP | Функции | — | `server.go` |
| Д1 | Traffic/process | Мониторинг | — | `diag_handlers.go` |
| Д2 | Latency history | Мониторинг | `internal/latency/*` | `servers_handlers.go`, `app.go` |
| Д3 | Tray health color | Мониторинг | — | `tray.go`, `icon.go`, `app.go` |
| Д4 | Traffic counter | Мониторинг | `internal/trafficstats/*` | `app.go` |
| Д5 | Crash report | Мониторинг | `internal/crashreport/*` | `app.go` |
| Д6 | Connection history | Мониторинг | `internal/connhistory/*` | `app.go` |
| Д7 | Memory watchdog | Мониторинг | — | `manager.go`, `app.go` |
| Д8 | Telemetry blocking | Мониторинг | — | `singbox_builder.go` |
