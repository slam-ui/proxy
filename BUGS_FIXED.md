# Все пофикшенные баги — proxy-fixed-v3 (обновлено)

---

## БАГ #1 — КРИТИЧЕСКИЙ 🔴
**Краш sing-box во время WaitForPort полностью теряется**

**Файл:** `internal/api/tun_handlers.go` → `doApply()`

**Суть:** После `xray.NewManager(...)` sing-box уже запущен, но `h.server.config.XRayManager`
устанавливался ПОСЛЕ `netutil.WaitForPort(15 секунд)`. `patchedCfg.OnCrash` читал
`srv.config.XRayManager` — пока шло ожидание там был `nil`. Если sing-box падал за эти
15 секунд (wintun-конфликт, bad config) — OnCrash видел `nil`, пропускал handleCrash,
retry loop не запускался. Прокси оставался отключённым навсегда до ручного перезапуска.

**Сценарий:** пользователь нажал Apply → sing-box упал через 2с → тишина в UI.

**Фикс:** Поменяли порядок — `SetXRayManager(newManager)` теперь вызывается сразу
после успешного `NewManager(...)`, ДО `WaitForPort`.

---

## БАГ #2 — ВАЖНЫЙ 🟠
**Backoff-горутина не прерывается при Shutdown приложения**

**Файл:** `internal/xray/manager.go` → `monitor()`

**Суть:** После краша sing-box вычислялся backoff (1с, 2с, 4с... до 30с) и запускалась горутина:
```go
go func() {
    time.Sleep(backoff)  // не прерывается!
    onCrash(err)
}()
```
При вызове `Shutdown()` приложения `lifecycleCtx` отменялся, но эта горутина продолжала
спать. Через backoff просыпалась и вызывала `onCrash` на уже останавливающемся приложении:
- `notification.Send()` — tray уже мог быть закрыт → паника
- `wintun.PollUntilFree()` — лишний вызов на завершении
- `apiServer.SetRestarting()` — запись в остановленный сервер

**Фикс:** Заменили `time.Sleep` на `select` с `m.lifecycleCtx.Done()`:
```go
select {
case <-time.After(backoff):
case <-m.lifecycleCtx.Done():
    return
}
```

---

## БАГ #3 — ВАЖНЫЙ 🟠
**handleImport не нормализует правила — импорт тихо ломает роутинг**

**Файл:** `internal/api/tun_handlers.go` → `handleImport()`

**Суть:** `handleAddRule` и `handleBulkReplaceRules` вызывали `NormalizeRuleValue()` и
`DetectRuleType()`. `handleImport` — нет, правила сохранялись как есть из JSON-файла.
Если пользователь экспортировал правила через другой клиент или редактировал вручную:
- `"value": "https://google.com"` → сохранялось с URL-префиксом → sing-box не матчил
- `"type": ""` или `"type": "domain"` при реальном значении `"192.168.1.1"` → неверный тип
- process rules приводились к lowercase: `"Telegram.exe"` → `"telegram.exe"` — не совпадало с реальным процессом на Windows

**Фикс:** Добавлена та же нормализация что и в `handleBulkReplaceRules` — перед сохранением
каждое правило проходит через `NormalizeRuleValue` + `DetectRuleType` + case-handling для process.

---

## БАГ #4 — UX 🟡
**doApply не вызывает SetRestarting — UI показывает пустой экран 30 секунд**

**Файл:** `internal/api/tun_handlers.go` → `doApply()`

**Суть:** При нажатии Apply в UI происходит:
1. Остановка sing-box
2. wintun cleanup: RemoveStaleTunAdapter + PollUntilFree (~30 секунд)
3. Генерация конфига + запуск sing-box

Во время шага 2 `/api/status` возвращал `running=false, warming=false, ready_at=0`.
UI показывал "Остановлен" без какого-либо индикатора прогресса. Пользователь не знал
что приложение работает — мог нажать Apply повторно или закрыть приложение.

В `handleCrash` эта же ситуация корректно обрабатывалась через `SetRestarting()`.

**Фикс:** В начало `doApply` добавлен вызов `h.server.SetRestarting(wintun.EstimateReadyAt())`
с `defer h.server.ClearRestarting()`. Теперь UI видит `warming=true` + таймер обратного отсчёта.

---

## БАГ #5 — RACE 🟡
**StartAfterManualCleanup: race condition с prevFirst между двумя блоками мьютекса**

**Файл:** `internal/xray/manager.go` → `StartAfterManualCleanup()`

**Суть:**
```go
m.mu.Lock()
prevFirst := m.firstStart
m.firstStart = true   // устанавливаем
m.mu.Unlock()         // ← освобождаем — ОКНО ДЛЯ RACE
m.crashes.Reset()
err := m.doStart()    // doStart захватит мьютекс заново
if err != nil {
    m.mu.Lock()
    m.firstStart = prevFirst  // восстанавливаем — слишком поздно
    m.mu.Unlock()
}
```
Между `Unlock()` и `doStart()` другая горутина могла: захватить `m.mu`, прочитать
`firstStart=true`, вызвать свой `doStart()` — пропустить `BeforeRestart` (wintun cleanup).
Итог: двойной запуск sing-box без очистки wintun → конфликт TUN.

Логика `prevFirst`/восстановления была иллюзорной защитой — восстановление происходило
после race-окна, а не защищало от него.

**Фикс:** Убрана логика `prevFirst` и двойного мьютексного блока. `firstStart=true`
устанавливается под единственным `Lock()`, `doStart()` читает его корректно.

---

## БАГ #6 — ВАЖНЫЙ 🟠
**proxy.Manager не синхронизируется с реестром Windows при старте**

**Файлы:** `internal/proxy/manager.go`, `internal/proxy/windows_proxy.go`

**Суть:** `NewManager()` всегда создавал менеджер с `enabled=false` независимо от
реального состояния реестра. Если приложение завершилось аварийно (panic, kill -9,
BSOD) с включённым прокси — при следующем запуске:
- Реестр: `ProxyEnable=1` (прокси включён)
- `manager.enabled=false` (менеджер думает что выключен)
- UI показывает "Прокси: выключен"
- `IsEnabled()` → `false`
- `tray.SetEnabled(false)` → иконка выключена
- Реальный трафик при этом идёт через sing-box (прокси в реестре всё ещё включён)

Пользователь нажимает "Включить" → `Enable()` → видит что `m.enabled && m.config == config` (false)
→ пишет в реестр повторно → OK. Но до этого момента рассинхрон между UI и системой.

**Фикс:** Добавлена `getSystemProxyState()` в `windows_proxy.go` — читает
`ProxyEnable` и `ProxyServer` из реестра. `NewManager()` вызывает её и инициализирует
`m.enabled` и `m.config` реальными значениями. Обновлён тест `TestNewManager` —
он не может предполагать что начальное состояние всегда `false`.

---

## БАГ #7 — НЕЗНАЧИТЕЛЬНЫЙ 🟢
**extractServerIP: DNS-горутина без context зависает при Shutdown**

**Файл:** `cmd/proxy-client/app.go` → `extractServerIP()`

**Суть:**
```go
go func() {
    addrs, err := net.LookupHost(host)  // системный DNS, таймаут до 30с
    ch <- result{addrs[0]}
}()
select {
case r := <-ch:    return r.ip
case <-time.After(3 * time.Second):  return ""
}
```
При Shutdown (который может происходить в `handleCrash` при Kill Switch) горутина
с `net.LookupHost` не получала сигнал об отмене. После `time.After(3s)` основной код
возвращал `""`, но горутина продолжала висеть до системного DNS-таймаута (30+ секунд).
В пиковых случаях это задерживало полное завершение процесса.

**Фикс:** Убрана горутина + select. Используется `net.DefaultResolver.LookupHost(ctx, host)`
с `context.WithTimeout(context.Background(), 3*time.Second)` — DNS завершается сам
по таймауту контекста, без утечки горутины.

---

## БАГ #NEW-1 — ВАЖНЫЙ 🟠
**handleBulkReplaceRules не приводит non-process правила к lowercase**

**Файл:** `internal/api/tun_handlers.go` → `handleBulkReplaceRules()`

**Суть:**
`handleAddRule` и `handleImport` правильно применяют:
```go
if ruleType != config.RuleTypeProcess {
    val = strings.ToLower(val)
}
req.Rules[i].Value = val
```
`handleBulkReplaceRules` пропускает этот шаг — сохраняет `val` после `NormalizeRuleValue`
без приведения к нижнему регистру для domain/IP правил.

Сценарий: пользователь перетаскивает правила в UI (drag-and-drop) → фронтенд вызывает
PUT /api/tun/rules → если какое-то правило содержало uppercase (например получено через
import из стороннего клиента, затем пересохранено через bulk) → sing-box не матчит домен.

**Фикс:** В `handleBulkReplaceRules`, в цикл нормализации добавить:
```go
ruleType := config.DetectRuleType(strings.ToLower(val))
if ruleType != config.RuleTypeProcess {
    val = strings.ToLower(val)
}
req.Rules[i].Value = val
req.Rules[i].Type = ruleType
```

---

## БАГ #NEW-2 — ВАЖНЫЙ 🟠
**engine_handlers.go: EnsureEngine использует context.Background() — не прерывается при Shutdown**

**Файл:** `internal/api/engine_handlers.go` → `handleEngineDownload()`

**Суть:**
```go
_ = engine.EnsureEngine(context.Background(), execPath, progress)
```
HTTP-таймаут в `engine.go`: `httpTimeout = 120 * time.Second`.
При Shutdown приложения:
1. `lifecycleCtx` отменяется
2. API Server Shutdown ждёт max 10с
3. Горутина загрузки sing-box.exe продолжает работать — HTTP-соединение не прерывается
4. Процесс не завершается до истечения HTTP-таймаута (до 2 минут)

Сравнение: singbox-launcher (аналогичный Go-проект) передаёт context в download — 
это стандартная практика для http-запросов в долгоживущих горутинах.

**Фикс:**
1. Добавить поле `server *Server` в замыкание `handleEngineDownload`
2. Использовать `s.lifecycleCtx` вместо `context.Background()`:
```go
_ = engine.EnsureEngine(s.lifecycleCtx, execPath, progress)
```

---

## БАГ #NEW-3 — RACE 🟡
**tray.OnQuit не защищён sync.Once — паника при двойном закрытии канала**

**Файл:** `cmd/proxy-client/main.go` → `run()`

**Суть:**
```go
OnQuit: func() { close(app.quit) },  // без sync.Once
```
`handleQuit` (POST /api/quit) использует `s.quitOnce.Do(...)` для защиты.
`OnQuit` из трея — не защищён. Если systray вызовет callback дважды (возможно при
быстром двойном клике на некоторых версиях Windows) → паника `close of closed channel`.

**Фикс:**
```go
var quitOnce sync.Once
// ...
OnQuit: func() {
    quitOnce.Do(func() { close(app.quit) })
},
```

---

## БАГ #NEW-4 — UX/ЛОГИКА 🟡
**handleCrash: Kill Switch включается и немедленно отключается при non-TUN краше**

**Файл:** `cmd/proxy-client/app.go` → `handleCrash()`

**Суть:**
Для non-TUN краша (например разрыв соединения с сервером):
```go
if a.cfg.KillSwitch {
    killswitch.Enable(...)   // 2× netsh → добавляет правила брандмауэра
}
proxyManager.Disable()
killswitch.Disable(...)      // 2× netsh → удаляет правила сразу же
```
4 лишних `netsh` вызова (~400-1200мс задержки). Краткое (~50мс) реальное блокирование
трафика между Enable и Disable. Kill Switch не несёт смысла при non-TUN краше т.к.
перезапуска не будет — прокси отключён до ручного рестарта.

**Фикс:** Убрать блок `killswitch.Enable` для non-TUN краша.
Оставить только `killswitch.Disable` (на случай если KS остался от предыдущей TUN-попытки):
```go
// Убрать:
// if a.cfg.KillSwitch {
//     serverIP := extractServerIP(a.cfg.SecretFile)
//     killswitch.Enable(serverIP, a.mainLogger)
// }

// Добавить в конец non-TUN блока:
killswitch.Disable(a.mainLogger)
```

---

## Итог

| # | Файл | Тип | Критичность | Статус |
|---|------|-----|-------------|--------|
| 1 | `api/tun_handlers.go` | Краш теряется при doApply | 🔴 Критический | ✅ Исправлен |
| 2 | `xray/manager.go` | Backoff-горутина без lifecycleCtx | 🟠 Важный | ✅ Исправлен |
| 3 | `api/tun_handlers.go` | handleImport не нормализует | 🟠 Важный | ✅ Исправлен |
| 6 | `proxy/manager.go` + `windows_proxy.go` | Рассинхрон с реестром при старте | 🟠 Важный | ✅ Исправлен |
| 4 | `api/tun_handlers.go` | doApply не вызывает SetRestarting | 🟡 UX | ✅ Исправлен |
| 5 | `xray/manager.go` | Race в StartAfterManualCleanup | 🟡 Race | ✅ Исправлен |
| 7 | `cmd/proxy-client/app.go` | DNS-горутина без context | 🟢 Незначительный | ✅ Исправлен |
| NEW-1 | `api/tun_handlers.go` | handleBulkReplaceRules не lowercase | 🟠 Важный | ✅ Исправлен |
| NEW-2 | `api/engine_handlers.go` | EnsureEngine с context.Background() | 🟠 Важный | ✅ Исправлен |
| NEW-3 | `cmd/proxy-client/main.go` | OnQuit без sync.Once | 🟡 Race | ✅ Исправлен |
| NEW-4 | `cmd/proxy-client/app.go` | KillSwitch Enable+Disable non-TUN | 🟡 UX/Logic | ✅ Исправлен |

**Изменённые файлы (v3):**
- `cmd/proxy-client/app.go`
- `internal/api/tun_handlers.go`
- `internal/xray/manager.go`
- `internal/proxy/manager.go`
- `internal/proxy/windows_proxy.go`
- `internal/proxy/manager_test.go`

**Исправлено в v4 (все NEW-1..4):**
- `internal/api/tun_handlers.go` (NEW-1)
- `internal/api/engine_handlers.go` (NEW-2)
- `cmd/proxy-client/main.go` (NEW-3)
- `cmd/proxy-client/app.go` (NEW-4)
