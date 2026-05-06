# AGENTS.md — SafeSky proxy-client

Project-level правила для SafeSky proxy-client.

Этот файл дополняет глобальные инструкции пользователя.
Если есть конфликт — этот файл выигрывает.

---

## 0. Hard rules for SafeSky

1. SafeSky — Windows 10/11 x64 client.
2. Не работать в dirty worktree.
3. Перед задачей читать project docs и audit-файлы.
4. Создавать feature branch перед изменениями.
5. Windows-only Go files должны иметь `//go:build windows`.
6. Для каждого Windows-only файла должен быть `*_other.go` stub, если пакет должен собираться на Linux.
7. HTTP JSON handlers должны использовать:
   - `http.MaxBytesReader`;
   - `json.Decoder`;
   - `DisallowUnknownFields`;
   - второй `Decode` для trailing data check.
8. API должен bind-иться только на `127.0.0.1`.
9. Frontend user data — только через `esc()` или `textContent`.
10. Не использовать native `alert/confirm/prompt` в UI.
11. Использовать `fetchWithTimeout`, не raw `fetch`.
12. Не логировать UUID/password/private keys/VLESS links целиком.
13. Не коммитить `dist/`, `.exe`, `.dll`, `secret.key`, пользовательские config-файлы.
14. Каждый багфикс — тест или явное объяснение, почему тест невозможен.
15. После Win32 правок обязательно:
    - `GOOS=windows go build ./...`
    - `GOOS=linux go build ./...`

---

## 1. Контекст проекта

SafeSky proxy-client — Windows-клиент для проксирования трафика на базе sing-box.

- Module: `proxyclient`
- Go: 1.24
- Toolchain: go1.26.2
- UI: WebView2 + HTML/CSS/JS
- Frontend:
  - `internal/api/static/index.html`
  - `internal/api/static/js/*.js`
- Platform: Windows 10/11 x64 only
- Engine: sing-box 1.13+
- Bundled binaries:
  - `sing-box.exe`
  - `wintun.dll`
- Build output:
  - `dist/`

---

## 2. Перед началом любой задачи

Выполнить:

```bash
git status --porcelain
```

Если worktree грязный — остановиться и спросить пользователя.

Прочитать перед изменениями:

```txt
BUGS_FIXED_AUDIT.md
BUG_REVIEW_NEEDED.md
docs/ARCHITECTURE.md
docs/BUGFIX_PROCESS_RULES.md
```

Если файла нет — не считать это ошибкой, но отметить в финальном отчёте.

Создать feature branch:

```bash
bugfix/<topic>
feat/<topic>
chore/<topic>
ui/<topic>
```

---

## 3. Платформозависимый код

### 3.1 — Windows-only files

Любой Go-файл с Win32 API должен начинаться с:

```go
//go:build windows
```

Примеры:

```txt
tray_win32.go
window_windows.go
procicon_handler_windows.go
hotkeys_windows.go
```

### 3.2 — Non-Windows stubs

Если пакет должен собираться на Linux, для Windows-only кода нужен stub:

```go
//go:build !windows

package api
```

Пример пары:

```txt
procicon_handler_windows.go
procicon_handler_other.go
tray_win32.go
tray_other.go
```

### 3.3 — Required builds

SafeSky is Windows-only, but Linux build is required to validate stubs.

После Win32/platform changes:

```bash
GOOS=windows go build ./...
GOOS=linux go build ./...
```

`GOOS=darwin go build ./...` не требуется для SafeSky, пока macOS support явно не добавлен.

---

## 4. Win32 API rules

### 4.1 — unsafe.Pointer

Правильно:

```go
proc.Call(uintptr(unsafe.Pointer(&data[0])), uintptr(size))
runtime.KeepAlive(data)
```

Правила:
- `unsafe.Pointer` использовать в том же выражении вызова, где возможно.
- Если данные должны жить до конца вызова — `runtime.KeepAlive(data)`.
- Размеры структур передавать как `uintptr(unsafe.Sizeof(x))`.
- `.Call(...)` принимает `uintptr`, не `uint32`.

### 4.2 — DLL loading

System DLL:

```go
windows.NewLazySystemDLL("kernel32.dll")
```

Не использовать:

```go
windows.NewLazyDLL("kernel32.dll")
```

User DLL / bundled DLL:

```go
exePath, err := os.Executable()
if err != nil {
    return err
}

dllPath := filepath.Join(filepath.Dir(exePath), "wintun.dll")
h, err := windows.LoadLibraryEx(
    dllPath,
    0,
    windows.LOAD_LIBRARY_SEARCH_APPLICATION_DIR,
)
```

### 4.3 — Callback functions

WndProc, hooks и Win32 callbacks всегда с recover первой логической операцией:

```go
func myCallback(hwnd, msg, wParam, lParam uintptr) (ret uintptr) {
    defer func() {
        if recover() != nil {
            ret = 0
        }
    }()

    // callback body
}
```

Callback должен храниться package-level:

```go
var trayWndProcCallback = syscall.NewCallback(trayWndProc)
```

Не создавать callback только в локальной переменной — GC может освободить его.

### 4.4 — HANDLE / GDI lifecycle

Каждый acquire должен иметь release через defer сразу после проверки ошибки.

```go
hIcon, _, _ := pExtractIcon.Call(...)
if hIcon == 0 {
    return errors.New("ExtractIcon failed")
}
defer ignoreWin32Call(pDestroyIcon, hIcon)
```

Resource table:

```txt
CreateFile / OpenProcess / CreateMutex       -> CloseHandle
RegOpenKey / RegCreateKey                    -> RegCloseKey
LoadLibrary / LoadLibraryEx                  -> FreeLibrary
LoadIcon / ExtractIcon / CreateIconFrom...   -> DestroyIcon
CreateBitmap / CreateDIBSection / LoadBitmap -> DeleteObject
GetDC                                        -> ReleaseDC
CreateCompatibleDC                           -> DeleteDC
LocalAlloc / GlobalAlloc                     -> LocalFree / GlobalFree
CoTaskMemAlloc                               -> CoTaskMemFree
MapViewOfFile                                -> UnmapViewOfFile
CreateWindowEx                               -> DestroyWindow
WintunCreateAdapter / WintunOpenAdapter      -> WintunCloseAdapter
CreateMenu / CreatePopupMenu                 -> DestroyMenu
```

---

## 5. HTTP API rules

HTTP API находится в:

```txt
internal/api/
```

Главная регистрация:

```txt
internal/api/server.go
```

### 5.1 — Routes

Новые routes добавлять только в `SetupFeatureRoutes()`.

Не добавлять новые routes в legacy `setupRoutes()`, если это не требуется для старого теста.

Пример:

```go
func (s *Server) SetupFeatureRoutes() {
    api.HandleFunc("/api/newroute", s.handleNewRoute).Methods("GET", "OPTIONS")
    s.addSilentPath("/api/newroute")
}
```

### 5.2 — Strict JSON body decoding

Каждый POST/PUT handler, который читает JSON body, должен использовать этот шаблон:

```go
r.Body = http.MaxBytesReader(w, r.Body, limit)

dec := json.NewDecoder(r.Body)
dec.DisallowUnknownFields()

if err := dec.Decode(&payload); err != nil {
    http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
    return
}

var extra struct{}
if err := dec.Decode(&extra); err != io.EOF {
    http.Error(w, "trailing data after JSON object", http.StatusBadRequest)
    return
}
```

Не использовать `dec.More()` для trailing data check после root object.

Рекомендуемые лимиты:

```txt
Small options:          4 KiB
Config JSON:           64 KiB
Profile/config import:  1 MiB
Bulk data:              4 MiB
```

### 5.3 — Path traversal

Любой handler, который принимает имя файла или путь, должен проверять traversal.

```go
clean := filepath.Clean(input)

if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
    http.Error(w, "invalid path", http.StatusBadRequest)
    return
}

abs, err := filepath.Abs(filepath.Join(rootDir, clean))
if err != nil {
    http.Error(w, "invalid path", http.StatusBadRequest)
    return
}

absRoot, err := filepath.Abs(rootDir)
if err != nil {
    http.Error(w, "invalid root", http.StatusInternalServerError)
    return
}

rel, err := filepath.Rel(absRoot, abs)
if err != nil || strings.HasPrefix(rel, "..") {
    http.Error(w, "path escapes root", http.StatusBadRequest)
    return
}
```

На Windows также запрещать reserved names:

```txt
CON
PRN
AUX
NUL
COM1-COM9
LPT1-LPT9
```

### 5.4 — Bind address

API должен bind-иться только на:

```txt
127.0.0.1
```

Запрещено:

```txt
0.0.0.0
::
localhost без явного resolve
```

### 5.5 — Silent paths

Часто опрашиваемые endpoints добавлять в silent paths, чтобы не зашумлять лог.

Обычно silent:

```txt
/api/stats
/api/connections
/api/servers
/api/geoip
/api/procicon
/api/diagnose
```

Пример:

```go
s.addSilentPath("/api/stats")
```

### 5.6 — API cache

Для дорогих операций нужен in-memory cache с TTL и max size:

```go
var (
    cacheMu  sync.Mutex
    cache    = map[string]*cacheEntry{}
    cacheTTL = 30 * time.Minute
    cacheMax = 256
)
```

Применимо к:
- process icons;
- geoip;
- server latency;
- expensive diagnostics.

---

## 6. Tests

### 6.1 — Existing tests must not break

Не ломать старые helpers и tests:

```txt
handlers_test.go
handlers_extra_test.go
servers_connect_test.go
server_lifecycle_test.go
```

Известные helpers:

```txt
postJSON
getJSON
deleteJSON
buildTunServer
buildServersServer
stubXray
stubProxy
mockXRayManager
mockProxyManager
```

Новые handlers через `SetupFeatureRoutes()` не должны требовать переписывания legacy tests без необходимости.

### 6.2 — Regression tests

Каждый bugfix требует тест.

Особенно:
- HTTP handlers → `httptest.NewRecorder`, `httptest.NewRequest`;
- concurrency → `-race -count=20`;
- config generation → sing-box check;
- parser bugs → table-driven tests;
- UI pure JS logic → если есть test harness, добавить test; если нет — указать manual verification.

### 6.3 — Goleak

Для пакетов с long-running goroutines использовать `goleak`, если уже подключён или уместен:

```go
defer goleak.VerifyNone(t)
```

Применимо:
- `internal/keepalive`;
- `internal/anomalylog`;
- `internal/netwatch`;
- `internal/process`;
- `internal/trafficstats`.

### 6.4 — sing-box check

При изменении:

```txt
internal/config/singbox_builder.go
internal/config/singbox_types.go
```

обязательно прогнать generated config через:

```bash
sing-box check -c <generated-config>
```

Если в тестах уже есть helper — использовать его.

### 6.5 — Golden tests

При изменении VLESS/sing-box config generation добавить golden tests для старых рабочих конфигов.

---

## 7. Logging

### 7.1 — No secret logs

Никогда не логировать целиком:

```txt
VLESS URL
UUID
password
private key
short_id
server key
subscription URL with token
config structs containing secrets
```

Использовать mask:

```go
func maskSecret(s string) string {
    if len(s) <= 8 {
        return "***"
    }
    return s[:4] + "..." + s[len(s)-4:]
}
```

### 7.2 — Log noise

Polling endpoints должны быть silent.

Не добавлять info log на каждый poll/tick.

### 7.3 — UI log normalization

Если изменяется `_normalizeLogKey(msg)`:
- добавлять новые паттерны;
- не удалять существующие без причины;
- следить, чтобы dedup не схлопывал разные реальные ошибки.

---

## 8. Frontend / WebView UI

Frontend находится:

```txt
internal/api/static/index.html
internal/api/static/js/*.js
```

### 8.1 — XSS protection

User data только через:

```js
textContent
esc()
```

Плохо:

```js
el.innerHTML = `<div>${userInput}</div>`;
```

Хорошо:

```js
el.textContent = userInput;
```

или:

```js
el.innerHTML = `<div>${esc(userInput)}</div>`;
```

Если нужно подсветить текст regex-ом:
1. сначала `esc()`;
2. потом вставлять safe `<span>` в escaped text.

### 8.2 — No native dialogs

Не использовать в production UI:

```js
alert()
confirm()
prompt()
window.alert()
window.confirm()
window.prompt()
```

Использовать:
- styled app modal;
- toast;
- inline validation.

Исключение: временный debug, не коммитить.

### 8.3 — fetchWithTimeout

Использовать:

```js
fetchWithTimeout(API + '/api/endpoint')
```

Не использовать raw:

```js
fetch('/api/endpoint')
```

### 8.4 — API constant

Всегда использовать `API` constant.

Плохо:

```js
fetch('http://127.0.0.1:8080/api/servers')
```

Хорошо:

```js
fetch(API + '/api/servers')
```

### 8.5 — Event listeners

Если listener ставится на stable element, созданный один раз — cleanup не обязателен.

Если listener ставится на dynamic/re-rendered element:
- использовать event delegation;
- или удалять старый listener;
- или гарантировать, что listener не дублируется.

### 8.6 — Layout safe areas

Все страницы с bottom nav должны иметь bottom padding в scroll container.

Контент не должен скрываться под нижней навигацией.

Все страницы с fixed/sticky header должны иметь top safe area.

### 8.7 — Scroll policy

`html` и `body` не должны получать случайный системный scroll.

Рекомендуемый принцип:

```css
html,
body {
  overflow: hidden;
  height: 100%;
}
```

Scroll только внутри page containers:

```css
.page-scroll,
.settings-scroll,
.logs-scroll,
.processes-scroll,
.rules-scroll {
  overflow-y: auto;
}
```

### 8.8 — UI polish commits

UI polish разрешён, если задача явно про:
- visual bug;
- UI polish;
- layout;
- design system;
- UX.

Не смешивать UI polish с backend/security changes в одном коммите.

### 8.9 — Buttons

Единая иерархия:
- primary — главное действие;
- secondary — дополнительное;
- danger — удаление/сброс/необратимое.

Danger не использовать для:
- import;
- export;
- update;
- check;
- open.

### 8.10 — Modals

Все modal/overlay должны иметь:
- styled header;
- close button справа;
- focus state;
- Escape close, если безопасно;
- backdrop click close только если это не destructive flow.

### 8.11 — Toast feedback

После save/apply/update действий должен быть feedback:
- success toast;
- error toast;
- inline validation для input errors.

### 8.12 — Country flags

WebView2 может плохо рендерить emoji flags.

Предпочтительно:
1. bundled/local flag assets;
2. fallback text country code;
3. remote CDN только если уже используется и нет локального набора.

Если используется remote flag CDN:
- обязательно fallback;
- UI не должен ломаться без интернета;
- не отправлять чувствительные server metadata во внешний сервис.

### 8.13 — Process icons

Process icons получать через local API:

```js
API + '/api/procicon?path=' + encodeURIComponent(exePath)
```

Всегда иметь fallback emoji/icon onerror.

---

## 9. Project structure

```txt
cmd/proxy-client/
  main.go
  versioninfo.json
  rsrc_windows_amd64.syso

internal/
  api/
    server.go
    static/index.html
    static/js/*.js

  tray/
  window/
  config/
  xray/
  proxy/
  wintun/
  logger/
  eventlog/
  engine/
  keepalive/
  latency/
  connhistory/
  trafficstats/
  netwatch/
  speedtest/
  notification/
  killswitch/
  apprules/
  dpapi/
  anomalylog/
  process/
  fileutil/
  netutil/
  autorun/
  clipboard/
  crashreport/
```

---

## 10. Key types

### 10.1 — Routing rules

```go
type RoutingRule struct {
    Value  string     `json:"value"`
    Type   RuleType   `json:"type"`   // domain | ip | process | geosite
    Action RuleAction `json:"action"` // proxy | direct | block
}
```

### 10.2 — Rules response

```go
type RulesResponse struct {
    DefaultAction RuleAction    `json:"default_action"`
    Rules         []RoutingRule `json:"rules"`
    DNS           *DNSConfig    `json:"dns,omitempty"`
}
```

### 10.3 — sing-box outbound transport

```go
type SBOutbound struct {
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

## 11. Build

### 11.1 — PowerShell build

Debug:

```powershell
.\build.ps1
```

Release:

```powershell
.\build.ps1 -Release
```

Skip tests only for quick local iteration:

```powershell
.\build.ps1 -SkipTests
```

### 11.2 — PowerShell script rules

PowerShell scripts must have:

```powershell
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
```

After external command invocation check `$LASTEXITCODE` if relevant.

### 11.3 — Build output

Output:

```txt
dist/
```

Do not commit:

```txt
dist/
dist/secret.key
*.exe
*.dll
```

### 11.4 — App icon

`goversioninfo` embeds `app_icon.ico` into `.exe` as resource ID=1.

Window/tray icon priority:

```txt
1. LoadImageW(hMod, 1, IMAGE_ICON, ...)
2. CreateIconFromResourceEx(...)
3. CreateIconFromResource(...)
```

Tray:

```txt
tray_win32.go -> buildIconHandle()
```

Window:

```txt
window/window.go -> setAppIcon(hwnd)
```

---

## 12. Audit files

### 12.1 — BUGS_FIXED_AUDIT.md

Use sequential `F-NNN`.

Format:

```md
### [F-NNN] <title>
- **Severity:** Critical | High | Medium | Low
- **Category:** <category>
- **File(s):** path:LINE
- **Commit:** <hash>
- **Symptom:** what was observed
- **Root cause:** why it happened
- **Fix:** what changed
- **Test:** test name
- **Verified:** build/test commands
```

### 12.2 — BUG_REVIEW_NEEDED.md

Use sequential `R-NNN`.

Format:

```md
## R-NNN: <title>
- **Coordinates:** file:line
- **Hypothesis:** what looks wrong
- **Why not fixed:** why not changed
- **Suggested fix:** proposed fix
- **Confidence:** Confirmed | Likely | Suspect
```

### 12.3 — .audit

For big passes:

```txt
.audit/<topic>/
  baseline_tests.txt
  final_tests.txt
  tool_versions.md
  staticcheck.txt
  gosec.txt
  govulncheck.txt
  coderabbit.txt
```

---

## 13. CodeRabbit

If available and authenticated:

```bash
coderabbit review --plain
```

If not available:
- do not block the pass;
- document in review-needed;
- continue with local tests/static analysis.

Triage:
- actionable → fix in separate commit;
- discussable → `CODERABBIT_DISCUSSION.md`;
- false positive → `CODERABBIT_IGNORED.md` with reason.

---

## 14. Current known state

Already fixed historically:
- strict JSON decoding for many handlers;
- concurrency bugs in anomalylog/proxy/xray/netwatch;
- Win32 DLL loading and callback recover issues;
- bind address hardening;
- backup path traversal;
- secret key zeroing;
- geoip hardening;
- document.write removal;
- fetchWithTimeout introduction;
- CI/security/lint improvements;
- VLESS transports support.

Known open product decision:
- kill switch policy requires product-level decision.

Before changing these areas, check:
- `BUGS_FIXED_AUDIT.md`;
- `BUG_REVIEW_NEEDED.md`;
- `docs/ARCHITECTURE.md`.

---

## 15. Frequent mistakes

| Mistake | Cause | Fix |
|---|---|---|
| Tray icon disappeared | `buildIconHandle` returned 0 | Check 3-tier icon loading and goversioninfo |
| Flags not visible | WebView2 emoji flags issue | Use local flag assets or image fallback |
| Geosite update loads too few bases | Rules not included | Read `/api/tun/rules` |
| Polling logs noisy | Missing silent path | `s.addSilentPath("/api/...")` |
| Tests fail on Linux | Win32 code without stub | Add `//go:build windows` + `_other.go` |
| `uint32` passed to `.Call` | Win32 proc expects uintptr | Convert to `uintptr` |
| sing-box fails after config change | schema drift | Run `sing-box check` |
| VLESS gRPC fails | unsupported mode | warn and fallback to supported mode |
| Crash in tray/menu | panic in callback | recover in callback |
| UI shows browser prompt | native `prompt()` used | replace with styled app modal |
| Content hidden under nav | missing bottom safe padding | add page scroll padding |
| Logs filled with polling | route not silent | add silent path |
| Secrets in logs | struct logged directly | log only safe fields / mask |
