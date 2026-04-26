# proxy-client

![Go](https://img.shields.io/badge/Go-1.24+-00ADD8?style=flat&logo=go)
![Platform](https://img.shields.io/badge/platform-Windows%2010%2F11-0078D4?style=flat&logo=windows)
![License](https://img.shields.io/badge/license-MIT-green?style=flat)
![Tests](https://img.shields.io/badge/tests-passing-brightgreen?style=flat)

Windows-клиент для проксирования трафика через **VLESS/Reality** с TUN-интерфейсом на базе [sing-box](https://github.com/SagerNet/sing-box).

Умеет маршрутизировать трафик по доменам, IP, процессам и geosite-категориям — через Web UI или напрямую через API.

---

## Возможности

- **TUN-интерфейс** — перехват трафика на уровне сети без настройки браузера
- **Гибкая маршрутизация** — proxy / direct / block по домену, IP/CIDR, `.exe`-процессу, geosite
- **Geosite** — готовые категории: youtube, discord, spotify, instagram, tiktok, reddit, soundcloud
- **Мониторинг процессов** — список запущенных `.exe` с отображением статуса маршрута
- **Web UI** — компактная панель управления на `localhost:8080`
- **Горячее применение правил** — без перезапуска клиента
- **Event log** — кольцевой буфер событий (500 записей) в реальном времени прямо в UI
- **Anomaly log** — автоматическая запись ошибок и крашей в файлы `anomaly-*.log`

---

## Требования

- Windows 10 / 11
- Go 1.24+
- [`sing-box.exe`](https://github.com/SagerNet/sing-box/releases) — положить в корень проекта
- Права администратора (для создания TUN-интерфейса)

---

## Установка

```powershell
git clone https://github.com/slam-ui/proxy.git
cd proxy

go mod download
.\build.ps1 -NoPause
```

---

## Запуск

### 1. Создать `secret.key`

Положить в корень файл `secret.key` с VLESS URL:

```
vless://uuid@host:port?security=reality&sni=example.com&pbk=...&sid=...&fp=chrome&flow=xtls-rprx-vision
```

Поддерживаются параметры:
- `flow=xtls-rprx-vision` — XTLS Vision (рекомендуется)
- `mux=1` или `multiplex=1` — мультиплексирование h2mux (8 потоков)
- Строки-комментарии (`# ...`) и UTF-8 BOM игнорируются

### 2. Запустить

```powershell
.\dist\SafeSky.exe
```

После запуска:

| | |
|---|---|
| Web UI | `http://localhost:8080` |
| HTTP прокси | `127.0.0.1:10807` |
| Clash API | `127.0.0.1:9090` |
| TUN-интерфейс | `tun0` (172.20.0.1/30) |

---

## Маршрутизация

Правила применяются **сверху вниз** — побеждает первое совпавшее.

| Тип | Пример | Описание |
|-----|--------|----------|
| `domain` | `google.com` | Точный домен или суффикс |
| `ip` | `8.8.8.8`, `10.0.0.0/8` | IP-адрес или CIDR |
| `process` | `chrome.exe` | Весь трафик процесса через TUN |
| `geosite` | `geosite:discord` | Категория доменов |

LAN-диапазоны (`192.168.0.0/16`, `10.0.0.0/8`, `127.0.0.0/8`) всегда идут напрямую — правило добавляется автоматически перед остальными.

> **Важно:** правила `direct` для конкретных доменов ставить **выше** правил `process`, иначе process-правило перехватит трафик раньше.

Пример `routing.json`:

```json
{
  "default_action": "direct",
  "rules": [
    { "value": "geosite:youtube",   "type": "geosite", "action": "proxy" },
    { "value": "geosite:discord",   "type": "geosite", "action": "proxy" },
    { "value": "geosite:spotify",   "type": "geosite", "action": "proxy" },
    { "value": "twitch.tv",         "type": "domain",  "action": "direct" },
    { "value": "firefox.exe",       "type": "process",  "action": "proxy" }
  ]
}
```

---

## Geosite

Файлы `geosite-*.bin` — скомпилированные rule-sets для sing-box.

Добавление новой категории:

```powershell
# Экспортировать категорию из базы (SagerNet/sing-geosite)
.\sing-box.exe geosite export <category> --file geosite.db --output geosite-<category>.srs

# Скомпилировать
.\sing-box.exe rule-set compile --output geosite-<category>.bin geosite-<category>.srs
```

Доступные категории: `youtube`, `google`, `telegram`, `twitter`, `netflix`, `spotify`, `discord`, `reddit`, `tiktok`, `instagram`, `github` и [сотни других](https://github.com/SagerNet/sing-geosite).

---

## API

<details>
<summary>Показать все эндпоинты</summary>

```
GET  /api/health               — healthcheck
GET  /api/status               — статус прокси и sing-box

POST /api/proxy/enable         — включить системный прокси
POST /api/proxy/disable        — выключить
POST /api/proxy/toggle

GET    /api/tun/rules          — список правил маршрутизации
PUT    /api/tun/rules          — заменить все правила
DELETE /api/tun/rules/:value   — удалить правило
POST   /api/tun/default        — изменить действие по умолчанию
POST   /api/tun/apply          — применить (перезапуск sing-box)

GET  /api/apps/processes       — список запущенных процессов со статусом
POST /api/apps/processes/refresh

GET  /api/events               — события (ring buffer 500, поддержка ?since=ID)

GET  /api/diag                 — диагностика (PID, uptime, память)
```

</details>

---

## Тестирование

```powershell
# Все тесты с race detector
go test -race -timeout=120s ./...

# Только кросс-платформенные пакеты (Linux/macOS)
go test -race -timeout=120s \
  ./internal/anomalylog/... \
  ./internal/apprules/... \
  ./internal/config/... \
  ./internal/eventlog/... \
  ./internal/logger/... \
  ./internal/netutil/...

# Покрытие с разбивкой по пакетам
go test -coverprofile=coverage.out -covermode=atomic \
  ./internal/anomalylog/... \
  ./internal/apprules/... \
  ./internal/config/... \
  ./internal/eventlog/... \
  ./internal/logger/... \
  ./internal/netutil/...
go tool cover -func=coverage.out
```

### Покрытые пакеты и что тестируется

| Пакет | Файлы тестов | Ключевые сценарии |
|-------|-------------|-------------------|
| `anomalylog` | `detector_test.go` | Crash/error/warn-burst детекция, ротация файлов, регрессии багов |
| `apprules` | `engine_test.go`, `storage_test.go`, `matcher_extra_test.go`, `storage_extra_test.go` | CRUD движка правил, сортировка по приоритету, NormalizePattern, Windows-пути, откат при ошибке сохранения, перезагрузка между сессиями |
| `config` | `singbox_test.go`, `tun_test.go`, `vless_extra_test.go` | Парсинг VLESS URL (mux, flow, BOM, комментарии), buildVLESSOutbound (TLS/Reality/XTLS), buildRoute, LAN bypass |
| `eventlog` | `eventlog_test.go`, `eventlog_extra_test.go` | Кольцевой буфер (overflow, GetSince после wrap), LineWriter (tab/CRLF trim, chunked write), Logger-адаптер, formatMsg |
| `logger` | `logger_test.go`, `logger_extra_test.go` | Полная матрица фильтрации уровней (16 комбинаций), Level.String(), отсутствие Sprintf-артефактов, параллельная безопасность |
| `netutil` | `port_test.go` | WaitForPort: быстрый путь, ожидание открытия, таймаут, нулевой таймаут, параллельность |

---

## Структура проекта

```
proxy/
├── cmd/proxy-client/
│   ├── main.go
│   └── app.go
├── internal/
│   ├── anomalylog/         # Детектор аномалий → файлы anomaly-*.log
│   ├── api/                # HTTP API + Web UI
│   │   ├── static/index.html
│   │   ├── server.go
│   │   ├── tun_handlers.go
│   │   ├── app_proxy_handlers.go
│   │   └── handlers_test.go
│   ├── apprules/           # Per-app rules engine (CRUD + persistence)
│   ├── config/             # Генератор конфига sing-box (VLESS, routing)
│   ├── engine/             # Оркестратор компонентов
│   ├── eventlog/           # Ring buffer для событий (O(1) insert, binary search)
│   ├── fileutil/           # Атомарная запись файлов (MoveFileExW)
│   ├── killswitch/         # Kill switch (Windows firewall)
│   ├── logger/
│   ├── netutil/            # WaitForPort с экспоненциальным backoff
│   ├── process/            # Монитор процессов (Windows)
│   ├── proxy/              # Управление системным прокси Windows
│   ├── wintun/             # Проверка наличия wintun.dll
│   └── xray/               # Менеджер процесса sing-box
├── .github/workflows/ci.yml
├── geosite-*.bin           # Скомпилированные rule-sets
├── secret.key              # VLESS URL (не коммитить!)
└── sing-box.exe            # sing-box binary (не коммитить!)
```

---

## CI/CD

GitHub Actions запускается на push/PR в `main` и вручную через `workflow_dispatch`.
Основной required check для branch protection — `CI Gate`; он ждёт все обязательные проверки.

| Джоб | Что делает |
|------|-----------|
| **Build** | `go mod verify`, `go mod tidy` check, `go vet`, Windows build artifact |
| **Test & Coverage** | `go test -race` для кросс-платформенных пакетов + `coverage.out` |
| **Test (Windows)** | native Windows build, API tests, Windows-only package tests |
| **Fuzz** | короткий regression fuzzing для `apprules` и `config` |
| **Lint** | `staticcheck` под Windows target |
| **Security Scan** | `gosec` SARIF + `govulncheck` JSON |
| **CodeQL** | GitHub security analysis |
| **Secret Scan** | Gitleaks — утечки секретов в коде |
| **License Check** | `go-licenses` — лицензии зависимостей |
| **CI Gate** | единый итоговый статус для branch protection |

---

## Устранение неполадок

**TUN не создаётся (`Cannot create a file when that file already exists`)**  
Интерфейс остался от предыдущего запуска. Удалите через `ncpa.cpl` или перезагрузите ПК. Клиент автоматически определяет этот краш и записывает `anomaly-*_crash.log`.

**Process-правило перехватывает весь трафик**  
Поставьте `direct`-правила для нужных доменов выше `process`-правила в списке.

**Geosite не работает**  
Убедитесь, что файл `geosite-<category>.bin` лежит рядом с `proxy-client.exe`.

**Прокси включился, но трафик не идёт**  
Проверьте `/api/status` — поле `xray.running` должно быть `true`. Если нет — смотрите event log в UI или файл `anomaly-*_error.log`.

---

## Лицензия

MIT
