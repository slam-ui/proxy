# AGENTS.md — правила работы с проектом SafeSky proxy-client

## Контекст проекта

Windows-клиент для проксирования трафика на базе sing-box.
- **Module:** `proxyclient`
- **Go:** 1.24 (toolchain go1.26.2)
- **UI:** WebView2 + HTML/JS, разбит на модули: `internal/api/static/index.html` + `internal/api/static/js/*.js`
- **Платформа:** Windows 10/11 x64 only
- **sing-box version:** 1.13+
- **Зависимости (binaries):** `sing-box.exe`, `wintun.dll` — лежат рядом с .exe

> Этот файл дополняет глобальный `~/.codex/AGENTS.md`. Если есть конфликт — этот выигрывает.

---

## 0. Перед началом любой задачи в этом проекте

1. Прочитать:
   - `BUGS_FIXED_AUDIT.md` — что уже исправлено (чтобы не дублировать).
   - `BUG_REVIEW_NEEDED.md` — что отложено и почему.
   - `docs/ARCHITECTURE.md` — общая архитектура.
   - `docs/BUGFIX_PROCESS_RULES.md` — процесс багфикса (если есть).
2. Проверить `git status --porcelain` — должен быть пусто.
3. Создать feature branch.

---

## 1. Платформозависимый код

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

Примеры: `procicon_handler_windows.go` / `procicon_handler_other.go`, `tray_win32.go` / `tray_other.go`.

После правок Win32 кода обязательно:
```bash
GOOS=windows go build ./...
GOOS=linux   go build ./...   # стабы валидны
```

---

## 2. Win32 API — правила вызовов

```go
// ПРАВИЛЬНО: unsafe.Pointer в том же выражении вызова
proc.Call(uintptr(unsafe.Pointer(&data[0])), uintptr(size))

// ПОСЛЕ вызова, если данные нужны — KeepAlive
runtime.KeepAlive(data)

// Размеры структур — uintptr, не uint32/uint64
uintptr(unsafe.Sizeof(myStruct))
```

Все Win32-проки объявляются как `var` на уровне пакета через `windows.NewLazySystemDLL` (для system DLL) или через explicit `LoadLibraryEx` с absolute path (для user DLL — wintun).

### 2.1 — Callback функции (WndProc, hooks)

**ВСЕГДА** с `defer recover()` первой строкой:

```go
func myCallback(hwnd, uMsg, wParam, lParam uintptr) (ret uintptr) {
    defer func() {
        if recover() != nil {
            ret = 0
        }
    }()
    // ... тело
}
```

Без recover паника в callback кладёт процесс целиком.

Callback должен иметь package-level хранение для lifetime:
```go
var trayWndProcCallback = syscall.NewCallback(trayWndProc) // package var, не локальная
```

Если создавать `NewCallback` в локальной переменной — Go GC может освободить, и Win32 вызовет protected memory.

### 2.2 — HANDLE/GDI lifecycle

Каждый acquire — парный cleanup в defer **сразу** после проверки ошибки:

```go
hIcon, _, _ := pExtractIcon.Call(...)
if hIcon == 0 {
    return errors.New("ExtractIcon failed")
}
defer ignoreProcIconCall(pDestroyIcon, hIcon)
```

Шаблон `ignoreProcIconCall`/`ignoreWin32Call` — игнорировать ошибку cleanup'а при выходе (всё равно ничего не сделать).

Чек-лист по типам ресурсов:
| Acquire | Release |
|---|---|
| `CreateFile`/`OpenProcess`/`CreateMutex` | `CloseHandle` |
| `RegOpenKey`/`RegCreateKey` | `RegCloseKey` |
| `LoadLibrary`/`LoadLibraryEx` | `FreeLibrary` (но не для system-DLL через `NewLazySystemDLL`) |
| `LoadIcon`/`ExtractIcon`/`CreateIconFromResource` | `DestroyIcon` |
| `CreateBitmap`/`CreateDIBSection`/`LoadBitmap` | `DeleteObject` |
| `GetDC`/`CreateCompatibleDC` | `ReleaseDC`/`DeleteDC` |
| `LocalAlloc`/`GlobalAlloc` | `LocalFree`/`GlobalFree` |
| `CoTaskMemAlloc` | `CoTaskMemFree` |
| `MapViewOfFile` | `UnmapViewOfFile` |
| `CreateWindowEx` | `DestroyWindow` |
| `WintunCreateAdapter`/`WintunOpenAdapter` | `WintunCloseAdapter` |
| `CreateMenu`/`CreatePopupMenu` | `DestroyMenu` |

---

## 3. HTTP API в `internal/api/`

### 3.1 — Регистрация маршрутов

Новые маршруты — **только** в `SetupFeatureRoutes()` в `server.go`, не в `setupRoutes()`.

```go
// В SetupFeatureRoutes():
api.HandleFunc("/newroute", s.handleNew).Methods("GET", "OPTIONS")
s.addSilentPath("/api/newroute") // если запросы частые/шумные
```

Это правило существует чтобы:
- Старые тесты, использующие `buildTunServer`/`buildServersServer`, не трогались.
- Новые routes легко находить.

### 3.2 — JSON body strict decoding

Каждый POST/PUT хендлер декодирующий body **обязательно**:

```go
r.Body = http.MaxBytesReader(w, r.Body, <разумный_лимит>)
dec := json.NewDecoder(r.Body)
dec.DisallowUnknownFields()
if err := dec.Decode(&payload); err != nil {
    http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
    return
}
if dec.More() {
    http.Error(w, "trailing data after JSON object", http.StatusBadRequest)
    return
}
```

Лимиты:
- Конфиг JSON: 64 KiB
- Опциональное body: 4 KiB
- Profile/большие конфиги: 1 MiB
- Bulk данные: 4 MiB

Уже сделано для F-001..F-006, F-014..F-019. Шаблон копировать оттуда.

### 3.3 — Path traversal — обязательная защита

Любой хендлер принимающий имя файла:

```go
clean := filepath.Clean(input)
if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
    http.Error(w, "invalid path", http.StatusBadRequest)
    return
}
abs, _ := filepath.Abs(filepath.Join(rootDir, clean))
absRoot, _ := filepath.Abs(rootDir)
rel, err := filepath.Rel(absRoot, abs)
if err != nil || strings.HasPrefix(rel, "..") {
    http.Error(w, "path escapes root", http.StatusBadRequest)
    return
}
```

Файлы на Windows: запретить также reserved names (CON, PRN, AUX, NUL, COM1-9, LPT1-9). Уже сделано в F-032.

### 3.4 — Bind address

API биндится **строго** на `127.0.0.1`. Не `0.0.0.0`, не `::`. Уже сделано в F-030.

### 3.5 — Silent paths

Часто-опрашиваемые endpoints (poll каждые 1-2 сек) **обязательно** в silent paths:
- `/api/stats` — Clash API статистика (~1s poll)
- `/api/connections` — список соединений (~2s poll)
- `/api/servers` — список серверов
- `/api/geoip` — геолокация IP
- `/api/procicon` — иконки процессов

Без silent path — лог захлёбывается через минуту.

### 3.6 — Кэширование в API

Для ресурсозатратных операций (извлечение иконок, геолокация) — in-memory кэш с TTL:

```go
var (
    cacheMu  sync.Mutex
    cache    = map[string]*cacheEntry{}
    cacheTTL = 30 * time.Minute
    cacheMax = 256
)
```

LRU eviction при превышении `cacheMax`.

---

## 4. Тесты

### 4.1 — Не ломать существующие

**Нельзя ломать**:
- `handlers_test.go` — без Windows constraint, использует `buildTunServer`
- `handlers_extra_test.go` — без Windows constraint
- `servers_connect_test.go` — без Windows constraint, использует `buildServersServer`

Тестовые helpers: `postJSON`, `getJSON`, `deleteJSON`, `buildTunServer`, `buildServersServer`.

Стабы: `stubXray` / `stubProxy` (`handlers_test.go`), `mockXRayManager` / `mockProxyManager` (`server_lifecycle_test.go`).

Новые хендлеры через `SetupFeatureRoutes` **не требуют** обновления существующих тестов.

### 4.2 — Регрессионные тесты обязательны

Каждый багфикс — с тестом, который падает до и проходит после. Особо для:
- Concurrency: `-race -count=20` (гонки вероятностные).
- HTTP handlers: тесты с `httptest.NewRecorder` + `httptest.NewRequest`.
- Win32 (где можно): `GOOS=windows` тесты.

### 4.3 — `goleak` для goroutine leak detection

Где есть долгоживущие горутины (`internal/keepalive`, `internal/anomalylog`, etc):

```go
import "go.uber.org/goleak"

func TestSomething(t *testing.T) {
    defer goleak.VerifyNone(t)
    // ... тест
}
```

### 4.4 — sing-box check для config-генерации

Любые изменения в `singbox_builder.go` или `singbox_types.go`:

```go
// Тест должен скармливать сгенерированный JSON в sing-box check
// См. TestSingBoxCheck_VLESSTransports как пример
```

Без sing-box check — schema drift пройдёт незамеченным.

### 4.5 — Golden tests для backward compat

При расширении функциональности (например, VLESS transports) — golden test для старых конфигов:

```go
// TestBuildVLESSOutbound_TCPRealityGolden — байт-в-байт сравнение
// JSON для исторически рабочего ключа
```

---

## 5. Логирование и шум

### 5.1 — Silent paths (см. 3.5)

### 5.2 — Маскирование секретов

Никогда не логировать `%v` от структур, содержащих UUID/private key. Маскировать:

```go
func maskSecret(s string) string {
    if len(s) <= 8 { return "***" }
    return s[:4] + "..." + s[len(s)-4:]
}
```

### 5.3 — UI-логи через `_normalizeLogKey`

`_normalizeLogKey(msg)` нормализует для дедупликации. При изменении **добавлять** паттерны, **не убирать** существующие. Текущие: порты, goroutine-ID, таймеры, IP, проценты, размеры файлов (MB/KB), скорость (MB/s).

---

## 6. Структура пакетов

```
cmd/proxy-client/          — точка входа; main.go, versioninfo.json, rsrc_windows_amd64.syso
internal/
  api/                     — HTTP сервер; server.go — главный файл с регистрацией роутов
                             static/index.html + static/js/*.js — frontend
  tray/                    — системный трей (Win32 Shell_NotifyIcon)
  window/                  — WebView2 окно; setAppIcon через LoadImageW(hMod, 1)
  config/                  — RoutingRule, RuleType, конфигурация sing-box
                             vless.go — парсер VLESS URL
                             singbox_builder.go — сборка outbound
                             singbox_types.go — структуры sing-box JSON
  xray/                    — управление sing-box процессом (исторически назван xray)
  proxy/                   — системный прокси (WinHTTP)
  wintun/                  — TUN интерфейс
  logger/, eventlog/       — логирование
  engine/                  — координация запуска (xray + proxy + wintun)
  keepalive/               — фоновый ping
  latency/                 — измерения
  connhistory/             — история соединений
  trafficstats/            — счётчики трафика
  netwatch/                — мониторинг сети
  speedtest/               — speedtest клиент
  notification/            — уведомления
  killswitch/              — WFP rules для блокировки трафика
  apprules/                — правила per-app
  dpapi/                   — Windows DPAPI шифрование
  anomalylog/              — детектор аномалий в логах
  process/                 — мониторинг процессов
  fileutil/                — atomic writes и т.д.
  netutil/                 — сетевые утилиты
  autorun/                 — автозапуск с Windows
  clipboard/               — буфер обмена
  crashreport/             — крашдампы
```

---

## 7. Ключевые типы

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

// config/singbox_types.go (после промта 08)
type SBOutbound struct {
    // ... поля для VLESS
    Transport *SBTransport `json:"transport,omitempty"`
}

type SBTransport struct {
    Type                string            `json:"type"`
    Path                string            `json:"path,omitempty"`
    Headers             map[string]string `json:"headers,omitempty"`
    MaxEarlyData        int               `json:"max_early_data,omitempty"`
    EarlyDataHeaderName string            `json:"early_data_header_name,omitempty"`
    Host                []string          `json:"host,omitempty"`
    Method              string            `json:"method,omitempty"`
    ServiceName         string            `json:"service_name,omitempty"`
}
```

---

## 8. Frontend (JS modules в `internal/api/static/js/`)

### 8.1 — XSS защита

При вставке user data в HTML **всегда** через `esc()` (определён в `00-core.js`):

```js
// плохо
el.innerHTML = `<div>${userInput}</div>`;

// хорошо
el.innerHTML = `<div>${esc(userInput)}</div>`;

// или ещё лучше для простых случаев
el.textContent = userInput;
```

`_highlightLogMsg` — корректный паттерн: сначала `esc()`, потом regex вставляет `<span>` теги в уже-escaped текст.

### 8.2 — Fetch с таймаутом

Использовать `fetchWithTimeout` (определён в `00-core.js` после F-036), не голый `fetch`:

```js
// автоматически с AbortController
const r = await fetchWithTimeout(API + '/endpoint');
```

Default timeout — 10 секунд.

### 8.3 — API const

Всегда через `API` константу (= `http://127.0.0.1:PORT`), не хардкодить:

```js
fetch(API + '/api/servers')
```

### 8.4 — Флаги стран

```js
function countryFlag(code) {
  const lc = code.toLowerCase(), uc = code.toUpperCase();
  return `<img src="https://flagcdn.com/w20/${lc}.png" class="flag-img" ...
    onerror="this.outerHTML='<span class=\\'cc-tag\\'>${uc}</span>'">`;
}
```

WebView2 не рендерит Unicode emoji-флаги — всегда через flagcdn.com.

### 8.5 — Иконки процессов

```js
// Через /api/procicon с emoji-fallback через onerror
const ico = exePath
  ? `<img src="${API}/procicon?path=${encodeURIComponent(exePath)}" ...
     onerror="this.outerHTML='${fallbackEmoji}'">`
  : fallbackEmoji;
```

### 8.6 — Event listeners

Сейчас 14 `addEventListener`, 0 `removeEventListener`. При добавлении нового listener:
- На стабильном элементе (создан раз при загрузке) — OK без cleanup.
- На динамически пересоздаваемом — обязательно cleanup или event delegation.

---

## 9. Сборка

```powershell
# Debug-сборка (сохраняет пользовательские данные)
.\build.ps1

# Release
.\build.ps1 -Release

# Пропустить тесты (только для быстрой итерации)
.\build.ps1 -SkipTests
```

**goversioninfo** вшивает `app_icon.ico` в .exe как ресурс ID=1 → иконка окна и трея берётся через `LoadImageW(hMod, 1, ...)`. Без этого иконки не работают.

Результат сборки: `dist/`. Не коммитить `dist/secret.key`.

PowerShell-скрипты должны иметь:
```powershell
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
```

И проверку `$LASTEXITCODE` после каждого `&` invocation.

---

## 10. Иконка приложения

Три слоя (от приоритетного к запасному):

1. **LoadImageW(hMod, 1, IMAGE_ICON, ...)** — из ресурсов .exe (требует goversioninfo)
2. **CreateIconFromResourceEx(png_bytes, ...)** — из ICO-байт встроенных в icon.go
3. **CreateIconFromResource(png_bytes, ...)** — старый fallback

Трей: `tray_win32.go → buildIconHandle()`
Окно: `window/window.go → setAppIcon(hwnd)`

---

## 11. Bug audit — формат отчёта

### 11.1 — `BUGS_FIXED_AUDIT.md`

Сквозная нумерация F-NNN. Секции по проходам (`## Concurrency pass`, `## Win32 / unsafe pass`, etc).

В TL;DR держать актуальные счётчики:
- Total fixed.
- По severity.
- gosec / lint / vuln deltas.

### 11.2 — `BUG_REVIEW_NEEDED.md`

R-NNN. Только спорные/политические/blocked внешними условиями.

### 11.3 — `.audit/<topic>/` для baseline

Структура (накопленная за 8 промтов):
```
.audit/
  baseline_*.txt       — пред-аудит снимки
  final_*.txt          — пост-аудит снимки
  concurrency/
    scan_log.md        — построчный лог сканирования
    race_baseline.txt
    race_final.txt
  win32/
  security/
  ui/
  triage/
  cleanup/
```

### 11.4 — CodeRabbit review

После каждого bugfix/audit pass запускать CodeRabbit и триажить результат по `docs/CODERABBIT_PROCESS.md`.
Actionable findings фиксируются отдельными коммитами, discussable — в `CODERABBIT_DISCUSSION.md`, false positives — в `CODERABBIT_IGNORED.md` с обоснованием.

---

## 12. Текущее состояние кодовой базы (на момент написания)

Уже исправлено (F-001..F-046):
- JSON decoders strict (11 хендлеров).
- Concurrency: anomalylog, proxy guard, xray restart, network watcher, feature route lifecycle, ping probes.
- Win32: DLL hijacking (system + wintun), callback recover (tray, netwatch), syscall conversions.
- Security: bind address 127.0.0.1, backup path traversal, reserved names, secret key zeroing, geoip hardening.
- UI: document.write removal, fetchWithTimeout.
- Lint: errcheck, CI security gates, PowerShell strict.
- VLESS transports: tcp, tcp+http-obf, ws, grpc, http/h2, httpupgrade. Backward compat golden test.

Остался открытым:
- R-003: kill switch policy (требует продуктового решения).

В работе по roadmap (после промта 28 cleanup):
- Phase 1: auto-update, leak protection, diagnostics, kill switch.
- Phase 2-5: extensions, UX, production.

---

## 13. Частые ошибки

| Ошибка | Причина | Решение |
|--------|---------|---------|
| Иконка трея исчезла | `buildIconHandle` вернул 0 | 3-tier в buildIconHandle; проверить goversioninfo |
| Флаги не отображаются | WebView2 не рендерит Unicode emoji-флаги | Всегда flagcdn.com img |
| Geosite update обновляет мало баз | `downloadGeosite` не читал rules | Читать `/api/tun/rules` тоже |
| Шумные логи при polling | Нет silent path | `s.addSilentPath("/api/...")` |
| Тест не компилируется не на Windows | Win32 код без build tag | `//go:build windows` + заглушка `_other.go` |
| `uint32` в `.Call(...)` | `.Call` принимает `...uintptr` | `uintptr(value)` |
| sing-box не запускается после правки config | Schema drift | Прогнать через `sing-box check` в тесте |
| VLESS gRPC ключ не работает | mode=multi не поддерживается | Warning в лог, использовать gun |
| Краш при выходе из tray menu | Паника в WndProc без recover | `defer recover()` (F-028) |

---

## 14. Полная рабочая память

Подробнее: `docs/ARCHITECTURE.md`, `docs/BUGFIX_PROCESS_RULES.md`.

Историческая миграция (что было раньше): `docs/BUGS_FIXED.md` — старый формат до текущего аудита.
