# 🗂 Быстрая навигация по проекту proxy-client

> **Справка для разработчиков и AI (Copilot/Claude)**  
> Дата: 2026-04-04 | Статус: ✓ Оптимизировано и очищено

---

## 🎯 Я хочу...

### Добавить новый API endpoint

1. **Выбери группу** (где он относится):
   - Серверы → `internal/api/servers_handlers.go` + `servers_handlers_*_test.go`
   - Настройки → `internal/api/settings_handlers.go`
   - Маршрутизация → `internal/api/engine_handlers.go`
   - TUN → `internal/api/tun_handlers.go`
   - Диагностика → `internal/api/diag_handlers.go`

2. **Добавь handler**:
   ```go
   func HandleGroupGet(w http.ResponseWriter, r *http.Request) {
       // Implement logic
       json.NewEncoder(w).Encode(result)
   }
   ```

3. **Зарегистрируй маршрут** в `internal/api/server.go`:
   ```go
   mux.HandleFunc("GET /api/group", HandleGroupGet)
   ```

4. **Добавь тесты** в соответствующий `*_test.go`

---

### Изменить логику маршрутизации

**Файл:** `internal/apprules/engine.go`

```go
// engine.go - ядро маршрутизации
func (e *Engine) Match(domain, ip string, processName string) (*Rule, error) {
    // Правила применяются сверху вниз
    // Первое совпадение побеждает
    for _, rule := range e.rules {
        if rule.Match(domain, ip, processName) {
            return rule, nil
        }
    }
    return nil, ErrNoMatch
}
```

**Тесты:** `internal/apprules/engine_test.go`, `engine_extra_test.go`, `engine_improved_test.go`

---

### Добавить новый тип правила

1. **Определи тип** в `internal/apprules/types.go`:
   ```go
   type Rule struct {
       Type   string // "domain", "ip", "process", "geosite", "my_type"
       Value  string
       Action string // "proxy", "direct", "block"
   }
   ```

2. **Реализуй matching** в `internal/apprules/matcher.go`

3. **Добавь тесты** (особенно Windows-специфика в `matcher_windows_test.go`)

4. **Обнови config builder** в `internal/config/singbox_builder.go`

---

### Исправить баг в обработке sing-box

**Файл:** `internal/xray/manager.go`

```go
// manager.go - управление процессом sing-box
type Manager struct {
    execPath string
    proc     *os.Process
    ctx      context.Context
    cancel   context.CancelFunc
}

func (m *Manager) Start() error {
    // Запустить sing-box.exe
    // Mониторить процесс
    // Перезапустить при сбое (exponential backoff)
}

func (m *Manager) Stop() error {
    // Graceful shutdown
}
```

**Ключевые баги уже исправлены:**
- ✓ Crash detection setup перед WaitForPort (BUG #1)
- ✓ Backoff goroutine cancellable via context (BUG #2)
- ✓ Win32 DLL lazy loading (BUG #20)

Смотри: [BUGS_FIXED.md](BUGS_FIXED.md)

---

### Улучшить конфигурацию

**Основные файлы:**

| Файл | Назначение |
|------|-----------|
| [internal/config/singbox_builder.go](internal/config/singbox_builder.go) | Генерирует JSON для sing-box.exe |
| [internal/config/singbox_types.go](internal/config/singbox_types.go) | Структуры для Marshaling |
| [internal/config/vless.go](internal/config/vless.go) | Парсинг VLESS URL |
| [internal/config/tun.go](internal/config/tun.go) | Конфиг TUN интерфейса |

**Пример - добавить новый параметр VLESS:**
```go
// vless.go
func ParseVLESS(url string) (*VLESSConfig, error) {
    // Распарсить vless://...
    // Извлечь uuid, host, port, security, sni, pbk, sid, fp, flow, mux
    // Валидировать
    return cfg, nil
}
```

---

### Добавить новый Windows-only пакет

**Паттерн (кроссплатформенность):**

```
internal/myfeature/
├─ feature.go          # Интерфейс/абстракция
├─ feature_windows.go  # Windows реализация
├─ feature_other.go    # Non-Windows stub (или panic)
├─ feature_test.go     # Базовые тесты
└─ feature_windows_test.go  # Platform-specific тесты
```

**Пример - notification:**
```go
// notification/notification_windows.go
//go:build windows

func ShowNotification(title, message string) error {
    // Windows Toast API
}

// notification/notification_other.go
//go:build !windows

func ShowNotification(title, message string) error {
    panic("notifications not supported on this platform")
}
```

---

## 📂 Структура на примерах

### Добавить обработчик для профилей

```
internal/
├─ api/
│  ├─ profile_handlers.go  ← Добавь новые handlers
│  └─ (нет отдельного *_test.go для profile, объедени с общим handlers_test.go)
│
└─ apprules/
   ├─ types.go             ← Определи тип Profile
   ├─ storage.go           ← Добавь методы сохранения профилей
   └─ storage_test.go      ← Добавь тесты
```

### Добавить новую диагностику

```
internal/
├─ api/
│  └─ diag_handlers.go     ← Добавь новый handler `/api/debug/profile`
└─ engine/
   ├─ engine.go            ← Добавь метод получения метрик
   └─ engine_test.go
```

---

## 🧪 Как запустить тесты

```powershell
# Все тесты (unit + race detector)
.\run_tests.ps1

# Только покрытие
.\run_tests.ps1 coverage
# Открыть coverage.html

# Только fuzz (60 сек)
.\run_tests.ps1 fuzz 60

# Все вместе
.\run_tests.ps1 all 60

# Конкретный пакет
go test -v ./internal/apprules

# Конкретный тест
go test -v -run "TestEngine" ./internal/apprules

# С race detector
go test -race -v ./internal/xray -timeout 30s
```

---

## 📚 Где найти что

| Что ищу | Где | Файл |
|--------|-----|------|
| HTTP сервер инициализация | API | `internal/api/server.go` |
| REST endpoints | API | `internal/api/*_handlers.go` |
| Маршрутизация логика | Rules Engine | `internal/apprules/engine.go` |
| Sing-box управление | Process | `internal/xray/manager.go` |
| TUN интерфейс | Network | `internal/wintun/wintun.go` |
| Windows прокси | System | `internal/proxy/manager.go` |
| Построение конфига | Config | `internal/config/singbox_builder.go` |
| Event log | Debugging | `internal/eventlog/eventlog.go` |
| Crash log | Debugging | `internal/anomalylog/detector.go` |
| Web UI | Frontend | `internal/api/static/` |
| Точка входа | Main | `cmd/proxy-client/main.go` |

---

## 🔍 Быстрый поиск

### Найти где вызывается функция

```bash
# Простой поиск
grep -r "FunctionName" internal/

# С номерами строк
grep -rn "FunctionName" internal/

# Только в Go файлах
grep -r "FunctionName" --include="*.go" internal/
```

### Найти все handlers

```bash
grep -r "^func Handle" internal/api/

# Только GET handlers
grep -r "GET" internal/api/*_handlers.go | grep "^func"
```

### Посмотреть использование пакета

```bash
# Что импортирует api/
grep -r "proxyclient/internal/api" internal/

# Что импортирует config/
grep -r "proxyclient/internal/config" internal/
```

---

## 🚀 Основные dev команды

```powershell
# Сборка
.\build.ps1           # Debug
.\build.ps1 -Release  # Release (оптимизировано)
.\build.ps1 -Clean    # Чистая сборка

# Разработка
.\dev.ps1 help        # Справка
.\dev.ps1 fmt         # Format code (gofmt)
.\dev.ps1 vet         # Vet analysis
.\dev.ps1 lint        # golangci-lint
.\dev.ps1 test        # Unit tests
.\dev.ps1 deps        # Показать зависимости

# Отладка
.\dev.ps1 run         # Debug run
.\monitor.ps1         # Мониторинг процесса
.\test-proxy.ps1      # Тест коннективности
```

---

## 🎓 Архитектурные паттерны

### Manager pattern

```go
type Manager interface {
    Start(ctx context.Context) error
    Stop() error
    IsRunning() bool
}

// Используется в:
// - internal/xray/Manager (sing-box)
// - internal/proxy/Manager (Windows proxy)
// - internal/turnmanager/Manager (TURN)
```

### Handler pattern (API)

```go
func HandleXGet(w http.ResponseWriter, r *http.Request) {
    if !isAuthed(r) { http.Error(w, "Unauthorized", 401); return }
    
    // Parse input
    id := r.URL.Query().Get("id")
    
    // Get data
    data, err := e.GetX(id)
    if err != nil { http.Error(w, err.Error(), 400); return }
    
    // Return JSON
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(data)
}
```

### Config builder pattern

```go
// internal/config/singbox_builder.go
func BuildConfig(rules []Rule) (*singbox.Config, error) {
    cfg := &singbox.Config{}
    
    for _, rule := range rules {
        cfg.Routes = append(cfg.Routes, buildRoute(rule))
    }
    
    return cfg, nil
}
```

---

## ❓ Частые вопросы

**Q: Где добавить новую настройку?**  
A: `internal/api/settings_handlers.go` (API) + `internal/config/` (persistence)

**Q: Как добавить новый тип логирования?**  
A: Либо в `internal/eventlog/` (runtime) либо `internal/anomalylog/` (errors)

**Q: Где проверяется авторизация?**  
A: `internal/api/handlers.go` в каждом handler или middleware (TBD - планируется извлечение)

**Q: Почему столько test files?**  
A: B5/B6/C5 версионирование от старых итераций разработки. Планируется консолидация.

**Q: Can я редактировать secret.key или routing.json?**  
A: Да, это user data. Не перезаписываются при сборке. Смотри `templates/` для defaults.

**Q: Как debug'ировать маршрутизацию?**  
A: Смотри Event Log в Web UI (localhost:8080) или check anomaly-*.log

---

## 📖 Справочная литература

| Документ | Для чего |
|----------|---------|
| [Readme.md](Readme.md) | Обзор features, установка, запуск |
| [PROJECT_STRUCTURE.md](PROJECT_STRUCTURE.md) | Подробная структура всех пакетов |
| [OPTIMIZATION_ROADMAP.md](OPTIMIZATION_ROADMAP.md) | План оптимизации & recommendations |
| [BUGS_FIXED.md](BUGS_FIXED.md) | История исправленных багов |
| [SECTION_B_CHANGES.md](SECTION_B_CHANGES.md) | Section B improvements |
| [.github/copilot-instructions.md](.github/copilot-instructions.md) | AI assistants instructions |

---

## 🛠 Инструменты

### Рекомендуемые расширения VS Code

```json
{
  "recommendations": [
    "golang.go",
    "ms-vscode.makefile-tools",
    "GitHub.copilot",
    "ESLint",
    "golang.tools"
  ]
}
```

### Go версия

```
✓ Go 1.24+
```

### Зависимости

```
github.com/SagerNet/sing-box (external binary, not go get)
```

---

## 💬 Сообщить о проблемах

**Если что-то сломалось:**

1. Смотри [BUGS_FIXED.md](BUGS_FIXED.md) - может быть уже известный баг
2. Запусти `.\run_tests.ps1` чтобы проверить regression
3. Смотри anomaly-*.log и event log в Web UI
4. Проверь Windows event viewer (Applications and Services > Application)

---

**Last updated:** 2026-04-04  
**Status:** ✓ Production Ready
