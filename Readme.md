# Proxy Client

Windows прокси-клиент с TUN-интерфейсом на базе **sing-box** и протоколом **VLESS/Reality**.

## Возможности

- 🚀 Автоматическая генерация конфигурации из VLESS URL
- 🌐 TUN-интерфейс — перехват трафика на уровне сети (без настройки браузера)
- 🔀 Гибкая маршрутизация — proxy / direct / block по доменам, IP, процессам, geosite
- 🗺️ Поддержка **geosite** — маршрутизация по категориям доменов (discord, youtube, spotify и др.)
- 🖥️ Web UI — панель управления на `localhost:8080`
- 📡 HTTP API для управления прокси и правилами
- 🔄 Горячее применение правил без перезапуска клиента
- ✅ Graceful shutdown

## Скриншот

> Web UI доступен по адресу `http://localhost:8080`

## Структура проекта

```
proxy/
├── cmd/
│   └── proxy-client/
│       └── main.go
├── internal/
│   ├── api/
│   │   ├── static/
│   │   │   └── index.html        # Web UI
│   │   ├── server.go             # HTTP API сервер
│   │   ├── tun_handlers.go       # Handlers для правил маршрутизации
│   │   └── app_proxy_handlers.go
│   ├── apprules/
│   │   ├── engine.go             # Rules engine
│   │   ├── matcher.go
│   │   ├── storage.go            # Хранилище правил
│   │   └── types.go
│   ├── config/
│   │   ├── singbox.go            # Генератор конфига sing-box
│   │   └── tun.go                # TUN конфигурация
│   ├── proxy/
│   │   └── manager.go            # Управление системным прокси
│   └── xray/
│       └── manager.go            # Менеджер процесса sing-box
├── geosite-discord.bin           # Скомпилированные geosite rule-sets
├── geosite-youtube.bin
├── geosite-spotify.bin
├── geosite-reddit.bin
├── geosite-tiktok.bin
├── geosite-instagram.bin
├── geosite-soundcloud.bin
├── routing.json                  # Правила маршрутизации
├── secret.key                    # VLESS URL (создаётся пользователем)
├── sing-box.exe                  # sing-box binary
└── go.mod
```

## Требования

- Windows 10/11
- Go 1.21+
- [sing-box](https://github.com/SagerNet/sing-box/releases) — положить `sing-box.exe` в корень проекта
- Права администратора (для TUN-интерфейса)

## Установка и сборка

```powershell
# Клонировать репозиторий
git clone <repo-url>
cd proxy

# Установить зависимости
go mod download

# Собрать
go build -o build/proxy-client.exe ./cmd/proxy-client
```

## Использование

### 1. Настройка

Создайте файл `secret.key` с вашим VLESS URL:

```
vless://uuid@server:port?security=reality&sni=example.com&pbk=publickey&sid=shortid&fp=chrome&flow=xtls-rprx-vision
```

### 2. Запуск

```powershell
.\build\proxy-client.exe
```

После запуска:
- Web UI: `http://localhost:8080`
- HTTP прокси: `127.0.0.1:10807`
- TUN-интерфейс: `tun1` (172.20.0.1/30)

### 3. Маршрутизация

Правила применяются **сверху вниз** — первое совпавшее правило выигрывает.

| Тип | Пример | Описание |
|-----|--------|----------|
| `domain` | `google.com` | Точный домен |
| `domain_suffix` | `.twitch.tv` | Домен и все поддомены |
| `process` | `chrome.exe` | Весь трафик процесса (через TUN) |
| `ip` | `8.8.8.8`, `10.0.0.0/8` | IP или CIDR |
| `geosite` | `geosite:discord` | Категория доменов |

**Важно:** правила `direct` должны стоять **выше** правил `process`, иначе process-правило перехватит весь трафик раньше.

Пример `routing.json`:
```json
{
  "default_action": "direct",
  "rules": [
    {"value": "twitch.tv",         "type": "domain",  "action": "direct"},
    {"value": "geosite:discord",   "type": "geosite", "action": "proxy"},
    {"value": "geosite:youtube",   "type": "geosite", "action": "proxy"},
    {"value": "geosite:spotify",   "type": "geosite", "action": "proxy"},
    {"value": "firefox.exe",       "type": "process", "action": "proxy"}
  ]
}
```

### 4. Geosite rule-sets

Файлы `geosite-*.bin` — скомпилированные rule-sets для sing-box. Для добавления новой категории:

```powershell
# 1. Скачать базу geosite (SagerNet/sing-geosite)
# 2. Экспортировать нужную категорию
.\sing-box.exe geosite export <category> --file geosite.db --output geosite-<category>.srs

# 3. Скомпилировать в бинарный формат
.\sing-box.exe rule-set compile --output geosite-<category>.bin geosite-<category>.srs
```

Доступные категории в базе: `youtube`, `google`, `telegram`, `twitter`, `facebook`, `netflix`, `spotify`, `discord`, `reddit`, `tiktok`, `instagram`, `github`, и [сотни других](https://github.com/SagerNet/sing-geosite).

## API

### Статус

```
GET /api/status
```

### Управление прокси

```
POST /api/proxy/enable
POST /api/proxy/disable
POST /api/proxy/toggle
```

### Правила маршрутизации

```
GET    /api/tun/rules              # Список правил
POST   /api/tun/rules              # Добавить правило
DELETE /api/tun/rules/:value       # Удалить правило
POST   /api/tun/default            # Изменить действие по умолчанию
POST   /api/tun/apply              # Применить правила (перезапуск sing-box)
```

Пример добавления правила:
```bash
curl -X POST http://localhost:8080/api/tun/rules \
  -H "Content-Type: application/json" \
  -d '{"value":"geosite:discord","type":"geosite","action":"proxy"}'
```

## Устранение неполадок

**TUN не создаётся (`Cannot create a file when that file already exists`)**
— Интерфейс `tun1` остался от предыдущего запуска. Перезагрузите ПК или удалите адаптер вручную через `ncpa.cpl`.

**Процесс-правило перехватывает весь трафик**
— Поставьте `direct`-правила для нужных доменов **выше** `process`-правила в списке.

**Geosite не работает**
— Убедитесь что файл `geosite-<category>.bin` существует рядом с `proxy-client.exe`.

## Лицензия

MIT