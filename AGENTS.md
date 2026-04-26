# AGENTS.md — правила работы с проектом SafeSky proxy-client

## Контекст проекта

Windows-клиент для проксирования трафика (VLESS/Reality + TUN) на базе sing-box.
- **Module:** `proxyclient`
- **Go:** 1.24
- **UI:** WebView2 + HTML/JS (`internal/api/static/index.html` — один файл, ~4000 строк)
- **Платформа:** Windows 10/11 x64 only

---

## Обязательные правила

### 1. Платформозависимый код

Весь код использующий Win32 API **должен** иметь build constraint:

```go
//go:build windows
```

Для каждого Windows-only файла **обязателен** файл-заглушка `*_other.go`:

```go
//go:build !windows
package api
// stub
```

Примеры: `procicon_handler_windows.go` / `procicon_handler_other.go`, `tray_win32.go`.

### 2. Win32 API — правила вызовов

```go
// ПРАВИЛЬНО: unsafe.Pointer в том же выражении вызова
proc.Call(uintptr(unsafe.Pointer(&data[0])), uintptr(size))

// ПОСЛЕ вызова, если данные нужны — KeepAlive
runtime.KeepAlive(data)

// Размеры структур — uintptr, не uint32/uint64
uintptr(unsafe.Sizeof(myStruct))
```

Все Win32-проки объявляются как `var` на уровне пакета через `windows.NewLazySystemDLL`.

### 3. Новые HTTP-маршруты

Регистрировать **только** в `SetupFeatureRoutes()` в `server.go`, не в `setupRoutes()`.

Шаблон добавления:
```go
// В SetupFeatureRoutes():
api.HandleFunc("/newroute", s.handleNew).Methods("GET", "OPTIONS")
s.addSilentPath("/api/newroute") // если запросы частые/шумные
```

Частые poll-эндпоинты (опрашиваются каждые ~1-2с) **всегда** добавлять в silent paths.

### 4. Тесты

**Нельзя ломать** существующие тесты. Перед любыми изменениями проверять:
- `handlers_test.go` — без Windows constraint, использует `buildTunServer`
- `handlers_extra_test.go` — без Windows constraint
- `servers_connect_test.go` — без Windows constraint, использует `buildServersServer`

Тестовые helper-функции: `postJSON`, `getJSON`, `deleteJSON`, `buildTunServer`, `buildServersServer`.

Стабы для тестов: `stubXray` / `stubProxy` (в `handlers_test.go`), `mockXRayManager` / `mockProxyManager` (в `server_lifecycle_test.go`).

Новые хендлеры, регистрируемые только через `SetupFeatureRoutes`, **не требуют** обновления существующих тестов.

### 5. Логирование и шум

Silent paths (не логировать в middleware) обязательны для:
- `/api/stats` — Clash API статистика (~1s poll)
- `/api/connections` — список соединений (~2s poll)
- `/api/servers` — список серверов
- `/api/geoip` — геолокация IP
- `/api/procicon` — иконки процессов

### 6. Кэширование в API хендлерах

Для ресурсозатратных операций (извлечение иконок, геолокация и т.д.) **обязателен** in-memory кэш с TTL. Шаблон:

```go
var (
    cacheMu  sync.Mutex
    cache    = map[string]*cacheEntry{}
    cacheTTL = 30 * time.Minute
    cacheMax = 256
)
```

---

## Структура пакетов

```
cmd/proxy-client/          — точка входа; main.go, versioninfo.json, rsrc_windows_amd64.syso
internal/
  api/                     — HTTP сервер; server.go — главный файл с регистрацией роутов
  tray/                    — системный трей (Win32 Shell_NotifyIcon)
  window/                  — WebView2 окно; setAppIcon через LoadImageW(hMod, 1)
  config/                  — RoutingRule, RuleType, конфигурация sing-box
  xray/                    — управление sing-box процессом
  proxy/                   — системный прокси (WinHTTP)
  wintun/                  — TUN интерфейс
  logger/, eventlog/       — логирование
  engine/                  — координация запуска (xray + proxy + wintun)
```

---

## Ключевые типы

```go
// config/tun.go
type RoutingRule struct {
    Value  string     `json:"value"`
    Type   RuleType   `json:"type"`   // "domain"|"ip"|"process"|"geosite"
    Action RuleAction `json:"action"` // "proxy"|"direct"|"block"
}

// GET /api/tun/rules возвращает:
type RulesResponse struct {
    DefaultAction RuleAction    `json:"default_action"`
    Rules         []RoutingRule `json:"rules"`
    DNS           *DNSConfig    `json:"dns,omitempty"`
}
```

---

## Frontend (index.html) — правила

### Флаги стран

```js
// Всегда через flagcdn.com с onerror-фолбэком на cc-tag
function countryFlag(code) {
  const lc = code.toLowerCase(), uc = code.toUpperCase();
  return `<img src="https://flagcdn.com/w20/${lc}.png" class="flag-img" ...
    onerror="this.outerHTML='<span class=\\'cc-tag\\'>${uc}</span>'">`;
}
```

### Иконки процессов

```js
// Всегда через /api/procicon с emoji-фолбэком через onerror
const ico = exePath
  ? `<img src="${API}/procicon?path=${encodeURIComponent(exePath)}" ...
     onerror="this.outerHTML='${fallbackEmoji}'">`
  : fallbackEmoji;
```

### Дедупликация логов

`_normalizeLogKey(msg)` — нормализует для сравнения. При изменении добавлять паттерны, а **не** убирать существующие. Текущие паттерны: порты, goroutine-ID, таймеры, IP, проценты, размеры файлов (MB/KB), скорость (MB/s).

### Fetch API

Всегда использовать `API` константу (не хардкодить localhost):
```js
const r = await fetch(API + '/endpoint');
```

---

## Сборка

```powershell
# Debug-сборка (сохраняет пользовательские данные)
.\build.ps1

# Release
.\build.ps1 -Release

# Пропустить тесты (только для быстрой итерации)
.\build.ps1 -SkipTests
```

**goversioninfo** вшивает `app_icon.ico` в .exe как ресурс ID=1 → иконка окна и трея берётся оттуда через `LoadImageW(hMod, 1, ...)`. Без этого шага иконки не работают.

Результат сборки: `dist/` (не коммитить `dist/secret.key`).

---

## Иконка приложения

Три слоя (от приоритетного к запасному):

1. **LoadImageW(hMod, 1, IMAGE_ICON, ...)** — из ресурсов .exe (требует goversioninfo при сборке)
2. **CreateIconFromResourceEx(png_bytes, ...)** — из ICO-байт встроенных в icon.go
3. **CreateIconFromResource(png_bytes, ...)** — старый fallback

Трей: `tray_win32.go → buildIconHandle()`
Окно: `window/window.go → setAppIcon(hwnd)`

---

## Частые ошибки

| Ошибка | Причина | Решение |
|--------|---------|---------|
| Иконка трея исчезла | `buildIconHandle` вернул 0 | 3-tier в buildIconHandle; проверить goversioninfo |
| Флаги не отображаются | WebView2 не рендерит Unicode emoji-флаги | Всегда flagcdn.com img |
| Geosite update обновляет мало баз | `downloadGeosite` не читал rules | Читать `/api/tun/rules` тоже |
| Шумные логи при polling | Нет silent path | `s.addSilentPath("/api/...")` |
| Тест не компилируется не на Windows | Win32 код без build tag | `//go:build windows` + заглушка `_other.go` |
| `uint32` в `.Call(...)` | `.Call` принимает `...uintptr` | `uintptr(value)` |

---

## Полная рабочая память

Подробнее: `.Codex/memory.md`
