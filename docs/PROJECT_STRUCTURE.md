# Оптимизированная структура проекта proxy-client

**Дата обновления:** 2026-04-04  
**Статус:** ✓ Очищено и оптимизировано

---

## 🎯 Обзор проекта

Это Windows-клиент прокси-маршрутизации трафика через **VLESS/Reality** с поддержкой TUN-интерфейса на базе sing-box.

```
Основные возможности:
✓ Гибкая маршрутизация (домен / IP / процесс / geosite)
✓ TUN-интерфейс без настройки браузера
✓ Web UI (localhost:8080) + REST API
✓ Event log + Anomaly log для отладки
✓ Горячее применение правил без перезагрузки
```

---

## 📁 Иерархия директорий

### Корневой уровень

```
.
├── cmd/                          ← Точки входа приложения
├── internal/                      ← Внутренние пакеты (основная логика)
├── templates/                     ← Шаблоны конфигов для новых установок
├── geosite/                       ← Категории доменов (загружаются автоматически)
├── build.ps1                      ← Главный скрипт сборки
├── run_tests.ps1 / test.ps1      ← Тестирование
├── dev.ps1                        ← Утилиты разработки
├── go.mod / go.sum               ← Зависимости
├── .gitignore                     ← Контроль версий
└── LICENSE, Readme.md, *.md      ← Документация
```

**Очищены:**
- ✓ coverage, coverage.html, coverage.out (переконвертируются)
- ✓ test-output/ (логи тестов переконвертируются)
- ✓ geosite-*.bin (переда-загружаются)
- ✓ cmd/proxy-client/wintun_stopped_at (timestamp)

---

## 📦 Структура cmd/ — Точки входа

| Файл | Назначение | Статус |
|------|-----------|--------|
| **cmd/proxy-client/** | Основное приложение |  |
| ├─ main.go | Entry point, обработка паник, singleton mutex | ✓ Core |
| ├─ app.go | AppConfig, управление жизненным циклом | ✓ Core |
| ├─ app_test.go | Юнит-тесты инициализации | ✓ Keep |
| ├─ orphan_test.go | Интеграционные тесты очистки | ✓ Keep |
| └─ rsrc_windows_amd64.syso | Ресурс иконки (auto-generated) | ✓ Auto |
| **cmd/vk-turn-relay/** | Отдельный TURN relay сервис | ✓ Optional |
| └─ main.go | TURN relay entry point | ✓ Keep |

---

## 🔌 Структура internal/ — Внутренние пакеты

### Критический путь: Инициализация → TUN → Sing-box → API

```
                    ┌─────────────────────────────┐
                    │   cmd/proxy-client/main.go  │
                    │   (Entry point)             │
                    └────────────┬────────────────┘
                                 │
            ┌────────────────────┼────────────────────┐
            │                    │                    │
        internal/api        internal/xray        internal/wintun
        (HTTP Server)      (sing-box lifecycle)  (TUN interface)
            │                    │                    │
            └────────────────────┼────────────────────┘
                                 │
                    ┌────────────┴────────────┐
                    │                         │
            internal/apprules          internal/config
            (Routing engine)           (Config builder)
                    │                         │
                    └────────────┬────────────┘
                                 │
                    internal/proxy / netutil
                    (Windows proxy + network)
```

---

## 📋 Справочник пакетов

### Уровень 1: API & Web UI

| Пакет | Файлы | Назначение |
|-------|-------|-----------|
| **api/** | 10 src + 9 test | Web UI (localhost:8080), REST API handlers |
| | server.go | HTTP сервер инициализация |
| | embed.go | Встроенные статические файлы |
| | handlers*.go | CRUD endpoints для серверов, настроек, TUN, geosite |
| | geosite_handlers.go + geositeupdate.go | Загрузка категорий доменов |
| | diag_handlers.go | Диагностика & статистика |
| | backup_handlers.go | Backup / restore конфигов |
| | fuzz_test.go | Fuzzing для robustness |
| **static/** | Web UI assets (CSS, JS, HTML) |  |

**Тесты:** handlers_test.go + handlers_extra_test.go + server_improved_test.go  
**Version tests (safe to consolidate):** servers_handlers_[B5|B6|C5]_test.go

---

### Уровень 2: Маршрутизация & Конфигурация

| Пакет | Назначение |
|-------|-----------|
| **apprules/** | 🎯 Движок маршрутизации |
| | engine.go | Core matching (domain, IP, process, geosite) |
| | matcher.go | Pattern matching с wildcard и CIDR |
| | storage.go | Сохранение/загрузка JSON правил |
| | types.go | Определения типов |
| **config/** | ⚙ Парсинг конфигурации |
| | singbox_builder.go | Генерация конфига для sing-box |
| | vless.go | Парсинг VLESS URL (host, port, ID) |
| | vless_cache_*.go | Кеширование VLESS |
| | tun.go | Конфигурация TUN интерфейса |
| | ports.go | Константы портов |

---

### Уровень 3: Жизненный цикл процессов

| Пакет | Назначение |
|-------|-----------|
| **xray/** | 🔄 Управление жизненным циклом sing-box |
| | manager.go | Запуск, мониторинг, обработка краша |
| | filter_writer.go | Фильтрация спама из логов sing-box |
| **wintun/** | 🖥 Windows TUN интерфейс |
| | wintun.go | Создание / удаление TUN |
| | probe_windows.go | Кеширование имени интерфейса |
| | startup_sim_test.go | Симуляция сценариев (800+ строк) |
| **process/** | 📊 Мониторинг процессов |
| | launcher.go | Запуск процессов |
| | monitor_windows.go | WMI мониторинг состояния |

---

### Уровень 4: Безопасность & Windows-специфика

| Пакет | Назначение |
|-------|-----------|
| **proxy/** | 🔒 Управление Windows прокси |
| | manager.go | Включение / отключение прокси |
| | windows_proxy.go | Low-level операции с реестром |
| | proxy_guard_test.go | Guard recovery (каждые 5s) |
| **killswitch/** | ⚠ Блокировка трафика при сбое |
| | killswitch_windows.go | Windows Firewall rule |
| | killswitch_other.go | Stub для non-Windows |
| **notification/** | 🔔 Toast уведомления |
| | notification_windows.go | Windows-специфика |
| | notification_other.go | Stub для других ОС |

---

### Уровень 5: Поддерживающие сервисы

| Пакет | Назначение |
|-------|-----------|
| **eventlog/** | 📝 Ring buffer событий (500 записей) |
| **anomalylog/** | ⚠ Auto-logging крашей & ошибок |
| **watchdog/** | 🐕 Процесс supervision & restart |
| **autorun/** | ▶ Auto-start при входе в Windows |
| **tray/** | 🎨 Системный трей & context menu |
| **window/** | 🪟 WebView2 управление |
| **logger/** | 📍 Structured logging |
| **netutil/** | 🌐 Port checking, DNS resolution |
| **fileutil/** | 📁 Atomic file writes |
| **turnmanager/** | 🔀 TURN сервер (альт. протокол) |
| **turnproxy/** | 🔀 TURN relay proxying |
| **engine/** | 🔧 Orch. layer (координация) |

---

## 🧪 Стратегия тестирования

### Текущий статус

| Область | Статус | Заметка |
|---------|--------|---------|
| **Unit tests** | ✓ Present | go test -race -timeout=120s ./... |
| **Coverage** | ✓ Tracked | coverage.html (regenerated) |
| **Fuzz tests** | ✓ Present | FuzzXxx функции в *_test.go |
| **Platform tests** | ✓ Platform-specific | matcher_windows_test.go, probe_windows.go |

### Тестовые файлы по типам

```
Standard:           *_test.go              (основные тесты)
Better coverage:    *_improved_test.go     (улучшенные edge cases)
Extra scenarios:    *_extra_test.go        (дополнительные сценарии)
Version history:    *_B5_test.go, etc.     (итерации разработки)
Platform-specific:  *_windows_test.go      (Windows-только)
```

### Рекомендации по консолидации

**Можно объединить:**
- `api/servers_handlers_[B5|B6|C5]_test.go` → если нет пересечений
- `proxy/manager_[test|improved|extra|extra2]_test.go` → 1-2 файла
- `eventlog/eventlog_[test|improved|extra|extra2]_test.go` → 1-2 файла

**Рекомендация:** Оставить пока как есть для исторического контекста, объединить в следующем цикле оптимизации.

---

## 🔄 Быстрый справочник

### Команды разработки

```powershell
# Тестирование
.\run_tests.ps1              # Юнит-тесты с race detector
.\run_tests.ps1 coverage     # Coverage report
.\run_tests.ps1 fuzz 60      # Fuzz тесты (60 сек)

# Сборка
.\build.ps1                  # Debug build
.\build.ps1 -Release         # Release (оптимизировано)
.\build.ps1 -Clean           # Чистая сборка

# Развитие
.\dev.ps1 help               # Справка
.\dev.ps1 fmt                # Format code
.\dev.ps1 vet                # Vet analysis
.\dev.ps1 lint               # Linter
```

### Основные файлы для редактирования

| Файл | Когда редактировать |
|------|----------------------|
| [cmd/proxy-client/main.go](cmd/proxy-client/main.go) | Обработка паник, lifecycle hooks |
| [cmd/proxy-client/app.go](cmd/proxy-client/app.go) | Инициализация, конфигурация |
| [internal/api/server.go](internal/api/server.go) | API endpoints |
| [internal/apprules/engine.go](internal/apprules/engine.go) | Логика маршрутизации |
| [internal/xray/manager.go](internal/xray/manager.go) | Контроль sing-box |
| [internal/config/singbox_builder.go](internal/config/singbox_builder.go) | Генерация конфига |

---

## 🧹 Результаты очистки (2026-04-04)

### Удалено

```
✓ coverage, coverage.html, coverage.out (тестовые артефакты)
✓ test-output/ (логи тестирования)
✓ geosite-*.bin (переда-загружаются)
✓ cmd/proxy-client/wintun_stopped_at (runtime marker)
✓ FILE_AUDIT_REPORT.md (audit artifact)
```

### Обновлено

```
✓ .gitignore — добавлены правила для тестовых артефактов
✓ PROJECT_STRUCTURE.md — создана (этот файл)
```

### Сохранено (user data)

```
✓ secret.key (VLESS URL конфиг)
✓ routing.json (правила маршрутизации)
✓ templates/ (шаблоны по умолчанию)
✓ *.md документация
```

---

## 📊 Статистика проекта

| Метрика | Значение |
|---------|----------|
| **Go source files** | 58 |
| **Test files** | 60 |
| **Active packages** | 24 |
| **Lines of Go code** | ~15,000+ |
| **Test variants** | 8 (B-series versioning) |
| **Platform-specific** | 7 (_windows.go + _other.go) |
| **Build time** | ~5-10 сек |
| **Test time** | ~30-60 сек (race + coverage) |

---

## 🔍 Заметки по архитектуре

### Ключевой поток данных

```
1. Запуск
   └─ main.go → app.go → create TUN → start sing-box → init API

2. Обновление правил
   ─ User edits via UI
   └─ POST /api/engine → validate → stop old → build new → start

3. Crashdet & recovery
   ─ sing-box dies
   └─ xray/manager detects → exponential backoff → restart

4. Shutdown
   └─ Graceful context cancellation → stop sing-box → cleanup
```

### Bug fixes документированы

```
• BUG #1  — sing-box crash detection during WaitForPort
• BUG #2  — Backoff goroutine not cancellable on shutdown
• BUG #20 — Win32 DLL loading in var-block
• BUG FIX series — Config validation, proxy guard recovery, HTTP test
```

Смотри: [BUGS_FIXED.md](BUGS_FIXED.md), [SECTION_B_CHANGES.md](SECTION_B_CHANGES.md)

---

## 🎯 Рекомендации на будущее

###短期 (Short-term)

1. **Consolidate test files** в массовом порядке если мешают (но сейчас полезны)
2. **Verify** manager_coverage_test.go — нужен ли его контент?
3. **Add TESTDATA docs** — объяснить testdata/ структуру

### 中期 (Mid-term)

1. **Split internal/api/** — слишком много handlers в одной папке
2. **Extract config logic** — часть может перейти в config/ пакет
3. **Add interface docs** — документировать контракты между пакетами

### 长期 (Long-term)

1. **Plugin architecture** — добавить custom routing rules
2. **Multi-protocol support** — не только VLESS/Reality
3. **Cross-platform** — подготовить к Linux/Mac (сейчас Windows-only)

---

## 📚 Файлы-ориентиры

- [Readme.md](Readme.md) — Русский/English обзор функций
- [BUGS_FIXED.md](BUGS_FIXED.md) — История багов и фиксов
- [SECTION_B_CHANGES.md](SECTION_B_CHANGES.md) — Улучшения (config validation, proxy guard)
- [.github/copilot-instructions.md](.github/copilot-instructions.md) — AI рекомендации
- **PROJECT_STRUCTURE.md** — Этот файл (навигация)

---

**Последнее обновление:** 2026-04-04  
**Статус оптимизации:** ✓ Complete, ready for use
