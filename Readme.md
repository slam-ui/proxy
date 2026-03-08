# proxy-client

![Go](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go)
![Platform](https://img.shields.io/badge/platform-Windows%2010%2F11-0078D4?style=flat&logo=windows)
![License](https://img.shields.io/badge/license-MIT-green?style=flat)

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
- **Event log** — лог событий в реальном времени прямо в UI

---

## Требования

- Windows 10 / 11
- Go 1.21+
- [`sing-box.exe`](https://github.com/SagerNet/sing-box/releases) — положить в корень проекта
- Права администратора (для создания TUN-интерфейса)

---

## Установка

```powershell
git clone https://github.com/slam-ui/proxy.git
cd proxy

go mod download
go build -o proxy-client.exe ./cmd/proxy-client
```

---

## Запуск

### 1. Создать `secret.key`

Положить в корень файл `secret.key` с VLESS URL:

```
vless://uuid@host:port?security=reality&sni=example.com&pbk=...&sid=...&fp=chrome&flow=xtls-rprx-vision
```

### 2. Запустить

```powershell
.\proxy-client.exe
```

После запуска:

| | |
|---|---|
| Web UI | `http://localhost:8080` |
| HTTP прокси | `127.0.0.1:10807` |
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
    { "value": "firefox.exe",       "type": "process", "action": "proxy" }
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

GET  /api/events               — последние события (ring buffer 500)
```

</details>

---

## Структура проекта

```
proxy/
├── cmd/proxy-client/
│   └── main.go
├── internal/
│   ├── api/
│   │   ├── static/index.html        # Web UI
│   │   ├── server.go
│   │   ├── tun_handlers.go
│   │   └── app_proxy_handlers.go
│   ├── apprules/                    # Per-app rules engine
│   ├── config/                      # Генератор конфига sing-box
│   ├── eventlog/                    # Ring buffer для событий
│   ├── logger/
│   ├── process/                     # Монитор процессов (Windows)
│   ├── proxy/                       # Управление системным прокси
│   └── xray/                        # Менеджер процесса sing-box
├── geosite-*.bin                    # Скомпилированные rule-sets
├── routing.json                     # Правила маршрутизации
├── secret.key                       # VLESS URL (не коммитить)
└── sing-box.exe                     # sing-box binary (не коммитить)
```

---

## Устранение неполадок

**TUN не создаётся (`Cannot create a file when that file already exists`)**  
Интерфейс остался от предыдущего запуска. Удалите через `ncpa.cpl` или перезагрузите ПК.

**Process-правило перехватывает весь трафик**  
Поставьте `direct`-правила для нужных доменов выше `process`-правила.

**Geosite не работает**  
Убедитесь, что файл `geosite-<category>.bin` лежит рядом с `proxy-client.exe`.

---

## Лицензия

MIT