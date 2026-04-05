# Архитектура proxy-client

**Дата:** 2026-04-05  
**Модуль:** `proxyclient` (Go 1.24)

---

## Слои приложения

```
┌──────────────────────────────────────────────────┐
│  Application Layer   cmd/proxy-client/           │  main, App lifecycle, AppConfig
├──────────────────────────────────────────────────┤
│  API Layer           internal/api/               │  REST handlers, Web UI, middleware
├──────────────────────────────────────────────────┤
│  Service Layer       internal/xray/              │  sing-box process management
│                      internal/config/            │  конфиг-билдер, парсер VLESS
│                      internal/apprules/          │  движок правил маршрутизации
│                      internal/engine/            │  автозагрузка sing-box с GitHub
├──────────────────────────────────────────────────┤
│  Support Layer       internal/logger/            │  абстракция логирования
│                      internal/eventlog/          │  ring-buffer событий UI
│                      internal/anomalylog/        │  детектор аномалий трафика
│                      internal/netutil/           │  сетевые утилиты (порты)
│                      internal/fileutil/          │  атомарная запись файлов
│                      internal/autorun/           │  автозапуск при старте Windows
│                      internal/killswitch/        │  блокировка трафика при краше
├──────────────────────────────────────────────────┤
│  Platform Layer      internal/wintun/            │  TUN-адаптер (Windows)
│                      internal/proxy/             │  системный HTTP-прокси Windows
│                      internal/process/           │  запуск дочерних процессов
│                      internal/notification/      │  Windows toast-уведомления
│                      internal/tray/              │  иконка в системном трее
│                      internal/window/            │  WebView2 окно приложения
│                      internal/turnproxy/         │  TURN/DTLS туннель (VK masquerade)
│                      internal/turnmanager/       │  управление TURN режимом
│                      internal/watchdog/          │  watchdog-процесс
└──────────────────────────────────────────────────┘
```

---

## Правила зависимостей

```
Platform → Support → Service → API → Application
```

- **Platform** не знает о Service/API — только о системных вызовах  
- **Service** зависит от интерфейсов, не от конкретных Platform реализаций  
- **API** получает зависимости через `api.Config` (dependency injection)  
- **Application** (`app.go`) собирает всё вместе — единственный файл с импортами всех слоёв  

---

## Ключевые интерфейсы

### `xray.Manager`
```go
type Manager interface {
    Start() error
    StartAfterManualCleanup() error
    Stop() error
    IsRunning() bool
    GetPID() int
    Wait() error
    LastOutput() string
    Uptime() time.Duration
    GetHealthStatus() (errorCount int, errorRatePct float64, wouldAlert bool)
}
```

### `proxy.Manager`
```go
type Manager interface {
    Enable(config Config) error
    Disable() error
    IsEnabled() bool
    GetConfig() Config
    StartGuard(ctx context.Context, interval time.Duration) error
    StopGuard()
}
```

### `logger.Logger`
```go
type Logger interface {
    Info(format string, args ...interface{})
    Error(format string, args ...interface{})
    Debug(format string, args ...interface{})
    Warn(format string, args ...interface{})
}
```

---

## Точки входа

| Бинарник | Путь | Назначение |
|----------|------|-----------|
| `proxy-client.exe` | `cmd/proxy-client/` | Основное приложение |
| `vk-turn-relay` | `cmd/vk-turn-relay/` | Серверный TURN relay |

---

## Граф зависимостей `api.Config`

```go
// api/server.go — все зависимости API слоя явны через Config:
type Config struct {
    ListenAddress      string
    XRayManager        xray.Manager        // Service layer
    ProxyManager       proxy.Manager       // Platform layer
    ConfigPath         string
    SecretKeyPath      string
    Logger             logger.Logger       // Support layer
    EventLog           *eventlog.Log       // Support layer
    QuitChan           chan struct{}
    SilentPaths        []string
    ManualTURNFn       func(bool) error    // Platform layer (turnmanager)
    SecretKeyUpdatedFn func()
}
```

---

## Тестовая стратегия

| Слой | Подход |
|------|--------|
| Platform (wintun, proxy) | `_other.go` stub + integration тест с реальным Windows API |
| Service (xray, config) | Unit тесты через интерфейсы, stub реализации |
| API | `httptest` + `stubXray` / `stubProxy` моки |
| Application | `app_test.go` интеграционный тест с полным lifecycle |

Stub-моки для API тестов находятся в `internal/api/handlers_test.go` (`stubXray`, `stubProxy`).

---

## Будущая целевая топология (Roadmap)

Детальный план — см. [TOPOLOGY_OPTIMIZATION.md](TOPOLOGY_OPTIMIZATION.md).

Краткий обзор:
- **Фаза 2**: `internal/core/` — сервис-локатор и DI-контейнер
- **Фаза 3**: разбить `internal/config/` на `config/builder/`, `config/parser/`
- **Фаза 4**: перегруппировать `internal/` → `platform/`, `services/`, `support/`
