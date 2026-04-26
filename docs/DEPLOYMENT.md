# 🚀 Инструкции по запуску proxy-client

**Статус:** ✓ Готово к использованию  
**Дата:** 2026-04-04  
**Версия:** Release  

---

## 📋 Что включено

```
✓ SafeSky.exe               — Основное приложение (результат `.\build.ps1`)
✓ sing-box.exe              — Прокси-движок (VLESS/Reality)
✓ wintun.dll                — Windows TUN драйвер
✓ secret.key                — Конфиг VLESS (нужна настройка!)
✓ routing.json              — Правила маршрутизации
✓ config шаблоны            — Для sing-box
```

---

## ⚙ Предварительная настройка

### 1. Подготовить VLESS URL

**Файл:** `secret.key` в корневой папке

Формат:
```
vless://UUID@host:port?security=reality&sni=example.com&pbk=PUBLIC_KEY&sid=SID_VALUE&fp=chrome&flow=xtls-rprx-vision
```

**Пример (скопируй и подставь свои значения):**
```
vless://550e8400-e29b-41d4-a716-446655440000@proxy.example.com:443?security=reality&sni=www.google.com&pbk=abc123...&sid=0&fp=chrome&flow=xtls-rprx-vision
```

### 2. Опционально: настроить маршруты

**Файл:** `routing.json` в корневой папке

**Пример:**
```json
{
  "default_action": "direct",
  "rules": [
    {
      "type": "geosite",
      "value": "youtube",
      "action": "proxy"
    },
    {
      "type": "domain",
      "value": "google.com",
      "action": "proxy"
    },
    {
      "type": "process",
      "value": "firefox.exe",
      "action": "proxy"
    },
    {
      "type": "ip",
      "value": "8.8.8.8",
      "action": "proxy"
    }
  ]
}
```

**Типы правил:**
- `domain` — Точный домен или суффикс (google.com = *.google.com)
- `ip` — IP-адрес или CIDR диапазон (192.168.0.0/16)
- `process` — .exe файл процесса (весь его трафик)
- `geosite` — Категория (youtube, discord, spotify, instagram, tiktok, reddit, soundcloud)

**Действия:**
- `proxy` — Через прокси
- `direct` — Напрямую
- `block` — Заблокировать

---

## 🎮 Запуск

### Способ 1: Графический интерфейс

```powershell
.\dist\SafeSky.exe
```

Откроется:
- ✓ System tray icon (панель задач)
- ✓ Web UI на `http://localhost:8080`
- ✓ Event log для отладки

### Способ 2: Консоль (отладка)

```powershell
.\dist\SafeSky.exe -NoGui
```

---

## 🕸 Веб-интерфейс

Откройте **`http://localhost:8080`** в браузере

### Доступные страницы

| Раздел | URL | Что там |
|--------|-----|---------|
| 📊 Dashboard | `/` | Статус приложения |
| 🖥 Серверы | `/servers` | Добавель/редактировать серверы |
| 🔀 Маршруты | `/engine` | Управление правилами маршрутизации |
| ⚙ Настройки | `/settings` | Конфиг приложения |
| 🌐 TUN | `/tun` | Статус TUN интерфейса |
| 📝 Logs | `/logs` | Event log (500 последних событий) |
| 📥 Backup | `/backup` | Сохранение/восстановление конфига |

---

## 🔌 API endpoints (для программистов)

**База:** `http://localhost:9090` (Clash API compatible)

```bash
# Получить текущие серверы
curl http://localhost:9090/api/servers

# Добавить правило маршрутизации
curl -X POST http://localhost:9090/api/engine/rules \
  -H "Content-Type: application/json" \
  -d '{"type":"domain","value":"youtube.com","action":"proxy"}'

# Получить статус TUN
curl http://localhost:9090/api/tun/status

# Скачать конфиг
curl http://localhost:9090/api/backup/export > config.json

# Загрузить конфиг
curl -X POST -F "file=@config.json" http://localhost:9090/api/backup/import
```

---

## 🐛 Отладка и логи

### Event Log (в реальном времени)

Откройте Web UI → `Logs` — видны последние 500 событий:
- ✓ Запуск/остановка приложения
- ✓ Ошибки конфигурации
- ✓ Timeout при подключении
- ✓ Crash и перезапуск процессов

### Файловые логи

```
anomaly-TIMESTAMP.log     — Ошибки и краши
proxy-client.log          — Основной лог (если включен)
```

### Консоль (если запущено с -NoGui)

```
[2026-04-04 10:15:32] ✓ App initialized
[2026-04-04 10:15:33] ✓ TUN interface created: tun0 (172.20.0.1)
[2026-04-04 10:15:34] ✓ sing-box process started (PID: 1234)
[2026-04-04 10:15:35] ✓ Web UI listening on http://localhost:8080
```

---

## 🔄 Частые действия

### Перезагрузить маршруты

**Web UI:** Settings → Reload Rules → Apply

Или via API:
```bash
curl -X POST http://localhost:9090/api/engine/reload
```

### Изменить VLESS сервер

1. Отредактируй `secret.key`
2. Перезагрузи приложение: `Ctrl+C` → запусти снова
3. Или: Web UI → Settings → Server → Edit → Save

### Посмотреть какие процессы маршрутизируются

**Web UI:** Dashboard → Processes

Покажет список `.exe` файлов и их маршруты:
```
chrome.exe        → proxy
firefox.exe       → proxy  
systemd.exe       → direct
```

### Отключить прокси на определённом процессе

**Web UI:** Add Rule → Type: process → Value: `processname.exe` → Action: direct

---

## ⚠ Общие проблемы

### Проблема: "TUN interface not available"

**Решение:**
```powershell
# Убедись что запущено с правами администратора
# Проверь что Windows 10/11
# Перезагрузись
# Если всё ещё не работает:
$env:PATH += ";C:\Windows\System32"
Get-NetAdapter | Where-Object {$_.Name -like "*tun*"}
```

### Проблема: "Connection timeout"

**Решение:**
1. Проверь VLESS URL в `secret.key` — он корректный?
2. Проверь доступ: `ping proxy.example.com`
3. Смотри Event Log → может быть ошибка парсинга URL
4. Попробуй с другим VLESS сервером

### Проблема: "Web UI not accessible on localhost:8080"

**Решение:**
```powershell
# Проверь что порт свободен
netstat -ano | grep 8080

# Проверь что приложение запущено
Get-Process proxy-client

# Проверь firewall
netsh advfirewall firewall show rule name="proxy-client" verbose
```

### Проблема: Crash и автоматический перезапуск

Это нормально! Приложение имеет exponential backoff:
- 1s wait → 2s wait → 4s wait → 8s wait → ... → 30s wait
- Смотри `anomaly-*.log` для деталей

---

## 🛡 Безопасность

### Защита доступа к Web UI

**Текущее:** Localhosts only (`127.0.0.1:8080`)

Если нужна сетевая доступность:
1. Отредактируй `api/server.go`
2. Измени `localhost` на `0.0.0.0`
3. Добавь аутентификацию (Bearer token или API key)

### Kill-switch для прокси

Если sing-box падает:
1. Приложение автоматически блокирует весь трафик (kill-switch)
2. Через 1-30 сек пытается перезапустить
3. Если не получается — весь трафик остаётся заблокирован для безопасности

Отключить kill-switch:
```powershell
.\dist\SafeSky.exe -NoKillswitch
```

---

## 📊 Мониторинг и метрики

### Доступные метрики в Event Log

```
Startup time       — Время запуска приложения
TUN creation time  — Время создания TUN интерфейса
sing-box ready     — Когда прокси готов к использованию
Restart count      — Сколько раз crashed и перезагрузился
Memory usage       — Текущее использование памяти
Processed packets  — Количество обработанных пакетов
```

### Экспорт статистики

```bash
curl http://localhost:9090/api/debug/stats | jq
```

---

## 🔧 Конфигурационные переменные

**Файл:** `config.runtime.json` (генерируется автоматически)

```json
{
  "app": {
    "listen_addr": "127.0.0.1:8080",
    "enable_gui": true,
    "enable_tray": true
  },
  "proxy": {
    "http_addr": "127.0.0.1:10807",
    "socks5_addr": "127.0.0.1:10808"
  },
  "tun": {
    "interface": "tun0",
    "mtu": 1500,
    "gateway": "172.20.0.1"
  }
}
```

---

## 📚 Дополнительные ресурсы

| Документ | Назначение |
|----------|-----------|
| [Readme.md](../Readme.md) | Основная информация о проекте |
| [PROJECT_STRUCTURE.md](../PROJECT_STRUCTURE.md) | Архитектура проекта для разработчиков |
| [QUICK_REFERENCE.md](../QUICK_REFERENCE.md) | Быстрая справка по коду |
| [OPTIMIZATION_ROADMAP.md](../OPTIMIZATION_ROADMAP.md) | План развития |
| [BUGS_FIXED.md](../BUGS_FIXED.md) | История исправленных багов |

---

## 💬 Получить помощь

### Если что-то не работает

1. **Проверь логи:** Web UI → Logs → последние события
2. **Проверь файлы:** `anomaly-*.log` в корневой папке
3. **Проверь консоль:** Запусти с `-NoGui` чтобы видеть вывод
4. **Перезагрузись:** Часто помогает `Ctrl+C` → запуск заново

### Для аналитики проблем

Сохрани информацию:
```powershell
# Сохранить конфиг
curl http://localhost:9090/api/backup/export > backup.json

# Сохранить логи
Copy-Item anomaly-*.log ./logs_export/

# Сохранить event log из UI
# (скопируй текст из Web UI → Logs)
```

---

## ✅ Готово!

```
┌─────────────────────────────────────────────┐
│  ✓ Приложение оптимизировано               │
│  ✓ Тесты пройдены                          │
│  ✓ Сборка выполнена                        │
│  ✓ Документация создана                    │
│                                             │
│  Для запуска:                              │
│  > .\dist\SafeSky.exe                      │
│                                             │
│  Затем откройте:                           │
│  > http://localhost:8080                   │
└─────────────────────────────────────────────┘
```

---

**Последнее обновление:** 2026-04-04  
**Статус:** Production Ready ✓
