# Proxy Client

Клиент для управления XRay прокси-сервером с поддержкой VLESS/Reality протокола.

---

## Что нужно скачать

### 1. Go — язык программирования

Нужен для сборки проекта из исходников.

| Платформа | Ссылка для скачивания |
|-----------|----------------------|
| **Windows x64** | [go1.21.6.windows-amd64.msi](https://go.dev/dl/go1.21.6.windows-amd64.msi) |
| **Linux x64** | [go1.21.6.linux-amd64.tar.gz](https://go.dev/dl/go1.21.6.linux-amd64.tar.gz) |
| **macOS ARM (M1/M2)** | [go1.21.6.darwin-arm64.pkg](https://go.dev/dl/go1.21.6.darwin-arm64.pkg) |
| **macOS Intel** | [go1.21.6.darwin-amd64.pkg](https://go.dev/dl/go1.21.6.darwin-amd64.pkg) |

> Все версии и платформы: [go.dev/dl](https://go.dev/dl/) — минимум **Go 1.21**

После установки проверьте в терминале:
```bash
go version
# go version go1.21.6 windows/amd64
```

---

### 2. XRay Core — ядро прокси

Основной исполняемый файл, который проксирует трафик.

| Платформа | Ссылка для скачивания |
|-----------|----------------------|
| **Windows x64** | [Xray-windows-64.zip](https://github.com/XTLS/Xray-core/releases/latest/download/Xray-windows-64.zip) |
| **Windows x86** | [Xray-windows-32.zip](https://github.com/XTLS/Xray-core/releases/latest/download/Xray-windows-32.zip) |
| **Linux x64** | [Xray-linux-64.zip](https://github.com/XTLS/Xray-core/releases/latest/download/Xray-linux-64.zip) |
| **macOS ARM (M1/M2)** | [Xray-macos-arm64.zip](https://github.com/XTLS/Xray-core/releases/latest/download/Xray-macos-arm64.zip) |
| **macOS Intel** | [Xray-macos-64.zip](https://github.com/XTLS/Xray-core/releases/latest/download/Xray-macos-64.zip) |

> Все релизы: [github.com/XTLS/Xray-core/releases/latest](https://github.com/XTLS/Xray-core/releases/latest)

После скачивания **распакуйте архив** и перенесите файл `xray.exe` (на Windows) или `xray` (на Linux/macOS) в папку `xray_core/` внутри проекта:

```
proxyclient/
└── xray_core/
    └── xray.exe        ← сюда
```

---

### 3. Git — для клонирования репозитория

| Платформа | Ссылка для скачивания |
|-----------|----------------------|
| **Windows** | [git-scm.com/download/win](https://git-scm.com/download/win) |
| **macOS** | устанавливается вместе с Xcode Command Line Tools: `xcode-select --install` |
| **Linux** | `sudo apt install git` или `sudo dnf install git` |

---

## Запуск проекта

### Шаг 1 — Склонируйте репозиторий

```bash
git clone <ссылка-на-репозиторий>
cd proxyclient
```

---

### Шаг 2 — Положите XRay Core в проект

Создайте папку `xray_core` и скопируйте туда скачанный `xray.exe`:

```
# Windows (PowerShell)
mkdir xray_core
# Перетащите xray.exe в папку xray_core вручную или:
Copy-Item "C:\Downloads\xray.exe" -Destination "xray_core\xray.exe"
```

```bash
# Linux / macOS
mkdir xray_core
cp ~/Downloads/xray xray_core/xray
chmod +x xray_core/xray    # сделать исполняемым
```

---

### Шаг 3 — Создайте файл `secret.key`

Создайте файл `secret.key` в корне проекта и вставьте в него ваш VLESS URL одной строкой:

```
vless://ваш-uuid@ваш-сервер.com:443?sni=example.com&pbk=ваш-publickey&sid=ваш-shortid
```

> VLESS URL выдаёт администратор VPN-сервера или панель управления (например, 3x-ui, Marzban).

---

### Шаг 4 — Установите зависимости

```bash
go mod download
```

---

### Шаг 5 — Соберите проект

**Windows (PowerShell):**
```powershell
.\build.ps1
```

**Или вручную на любой платформе:**
```bash
go build -o proxy-client.exe ./cmd/proxy-client    # Windows
go build -o proxy-client ./cmd/proxy-client        # Linux / macOS
```

---

### Шаг 6 — Запустите

> ⚠️ На Windows нужны права **администратора** — программа изменяет системные настройки прокси.

**Windows — правой кнопкой → «Запуск от имени администратора»:**
```powershell
.\build\proxy-client.exe
```

**Linux / macOS:**
```bash
sudo ./proxy-client
```

После запуска в консоли появится:
```
[INFO] Конфигурация успешно сгенерирована
[INFO] Системный прокси включён: 127.0.0.1:10807
[INFO] XRay успешно запущен с PID: 12345
[INFO] API сервер запущен на :8080
```

---

### Шаг 7 — Откройте веб-интерфейс

Перейдите в браузере по адресу:

```
http://localhost:8080
```

Вы увидите панель управления с кнопкой включения/отключения прокси.

---

## Остановка

Нажмите `Ctrl+C` в терминале — программа корректно завершится, отключит системный прокси и остановит XRay.

---

## API

Все запросы идут на `http://localhost:8080`.

| Метод | Эндпоинт | Описание |
|-------|----------|----------|
| `GET` | `/api/status` | Текущий статус прокси и XRay |
| `GET` | `/api/health` | Проверка что сервер жив |
| `POST` | `/api/proxy/toggle` | Переключить прокси (вкл → выкл или выкл → вкл) |
| `POST` | `/api/proxy/enable` | Включить прокси |
| `POST` | `/api/proxy/disable` | Отключить прокси |

Пример ответа `GET /api/status`:
```json
{
  "xray":  { "running": true, "pid": 12345 },
  "proxy": { "enabled": true, "address": "127.0.0.1:10807" },
  "config_path": "config.runtime.json"
}
```

---

## Структура проекта

```
proxyclient/
├── cmd/proxy-client/
│   └── main.go                 — точка входа
├── internal/
│   ├── api/server.go           — HTTP API + CORS
│   ├── config/generator.go     — генерация конфига из VLESS URL
│   ├── logger/logger.go        — логгер
│   ├── proxy/manager.go        — управление системным прокси
│   ├── proxy/windows_proxy.go  — работа с реестром Windows
│   └── xray/manager.go         — запуск и остановка XRay
├── xray_core/
│   └── xray.exe                ← скачать и положить сюда
├── config.template.json        — шаблон конфигурации XRay
├── secret.key                  ← создать вручную, в git не попадёт
├── index.html                  — веб-интерфейс
└── go.mod
```

---

## Устранение неполадок

**`xray.exe не найден`** — убедитесь, что файл лежит точно по пути `xray_core/xray.exe`, а не в подпапке архива.

**`Системный прокси не включается`** — запустите `proxy-client.exe` от имени администратора.

**`Порт 8080 занят`** — другое приложение использует этот порт. Завершите его или измените `apiListenAddress` в `cmd/proxy-client/main.go`.

**`Фронтенд не видит API`** — открывайте `http://localhost:8080`, а не через `file://`. При открытии через `file://` браузер блокирует запросы из-за CORS.

**`secret.key: неверный формат`** — VLESS URL должен начинаться с `vless://` и содержать параметры `sni`, `pbk`, `sid`.# Proxy Client

Клиент для управления XRay прокси-сервером с поддержкой VLESS/Reality протокола.

---

## Что нужно скачать

### 1. Go — язык программирования

Нужен для сборки проекта из исходников.

| Платформа | Ссылка для скачивания |
|-----------|----------------------|
| **Windows x64** | [go1.21.6.windows-amd64.msi](https://go.dev/dl/go1.21.6.windows-amd64.msi) |
| **Linux x64** | [go1.21.6.linux-amd64.tar.gz](https://go.dev/dl/go1.21.6.linux-amd64.tar.gz) |
| **macOS ARM (M1/M2)** | [go1.21.6.darwin-arm64.pkg](https://go.dev/dl/go1.21.6.darwin-arm64.pkg) |
| **macOS Intel** | [go1.21.6.darwin-amd64.pkg](https://go.dev/dl/go1.21.6.darwin-amd64.pkg) |

> Все версии и платформы: [go.dev/dl](https://go.dev/dl/) — минимум **Go 1.21**

После установки проверьте в терминале:
```bash
go version
# go version go1.21.6 windows/amd64
```

---

### 2. XRay Core — ядро прокси

Основной исполняемый файл, который проксирует трафик.

| Платформа | Ссылка для скачивания |
|-----------|----------------------|
| **Windows x64** | [Xray-windows-64.zip](https://github.com/XTLS/Xray-core/releases/latest/download/Xray-windows-64.zip) |
| **Windows x86** | [Xray-windows-32.zip](https://github.com/XTLS/Xray-core/releases/latest/download/Xray-windows-32.zip) |
| **Linux x64** | [Xray-linux-64.zip](https://github.com/XTLS/Xray-core/releases/latest/download/Xray-linux-64.zip) |
| **macOS ARM (M1/M2)** | [Xray-macos-arm64.zip](https://github.com/XTLS/Xray-core/releases/latest/download/Xray-macos-arm64.zip) |
| **macOS Intel** | [Xray-macos-64.zip](https://github.com/XTLS/Xray-core/releases/latest/download/Xray-macos-64.zip) |

> Все релизы: [github.com/XTLS/Xray-core/releases/latest](https://github.com/XTLS/Xray-core/releases/latest)

После скачивания **распакуйте архив** и перенесите файл `xray.exe` (на Windows) или `xray` (на Linux/macOS) в папку `xray_core/` внутри проекта:

```
proxyclient/
└── xray_core/
    └── xray.exe        ← сюда
```

---

### 3. Git — для клонирования репозитория

| Платформа | Ссылка для скачивания |
|-----------|----------------------|
| **Windows** | [git-scm.com/download/win](https://git-scm.com/download/win) |
| **macOS** | устанавливается вместе с Xcode Command Line Tools: `xcode-select --install` |
| **Linux** | `sudo apt install git` или `sudo dnf install git` |

---

## Запуск проекта

### Шаг 1 — Склонируйте репозиторий

```bash
git clone <ссылка-на-репозиторий>
cd proxyclient
```

---

### Шаг 2 — Положите XRay Core в проект

Создайте папку `xray_core` и скопируйте туда скачанный `xray.exe`:

```
# Windows (PowerShell)
mkdir xray_core
# Перетащите xray.exe в папку xray_core вручную или:
Copy-Item "C:\Downloads\xray.exe" -Destination "xray_core\xray.exe"
```

```bash
# Linux / macOS
mkdir xray_core
cp ~/Downloads/xray xray_core/xray
chmod +x xray_core/xray    # сделать исполняемым
```

---

### Шаг 3 — Создайте файл `secret.key`

Создайте файл `secret.key` в корне проекта и вставьте в него ваш VLESS URL одной строкой:

```
vless://ваш-uuid@ваш-сервер.com:443?sni=example.com&pbk=ваш-publickey&sid=ваш-shortid
```

> VLESS URL выдаёт администратор VPN-сервера или панель управления (например, 3x-ui, Marzban).

---

### Шаг 4 — Установите зависимости

```bash
go mod download
```

---

### Шаг 5 — Соберите проект

**Windows (PowerShell):**
```powershell
.\build.ps1
```

**Или вручную на любой платформе:**
```bash
go build -o proxy-client.exe ./cmd/proxy-client    # Windows
go build -o proxy-client ./cmd/proxy-client        # Linux / macOS
```

---

### Шаг 6 — Запустите

> ⚠️ На Windows нужны права **администратора** — программа изменяет системные настройки прокси.

**Windows — правой кнопкой → «Запуск от имени администратора»:**
```powershell
.\build\proxy-client.exe
```

**Linux / macOS:**
```bash
sudo ./proxy-client
```

После запуска в консоли появится:
```
[INFO] Конфигурация успешно сгенерирована
[INFO] Системный прокси включён: 127.0.0.1:10807
[INFO] XRay успешно запущен с PID: 12345
[INFO] API сервер запущен на :8080
```

---

### Шаг 7 — Откройте веб-интерфейс

Перейдите в браузере по адресу:

```
http://localhost:8080
```

Вы увидите панель управления с кнопкой включения/отключения прокси.

---

## Остановка

Нажмите `Ctrl+C` в терминале — программа корректно завершится, отключит системный прокси и остановит XRay.

---

## API

Все запросы идут на `http://localhost:8080`.

| Метод | Эндпоинт | Описание |
|-------|----------|----------|
| `GET` | `/api/status` | Текущий статус прокси и XRay |
| `GET` | `/api/health` | Проверка что сервер жив |
| `POST` | `/api/proxy/toggle` | Переключить прокси (вкл → выкл или выкл → вкл) |
| `POST` | `/api/proxy/enable` | Включить прокси |
| `POST` | `/api/proxy/disable` | Отключить прокси |

Пример ответа `GET /api/status`:
```json
{
  "xray":  { "running": true, "pid": 12345 },
  "proxy": { "enabled": true, "address": "127.0.0.1:10807" },
  "config_path": "config.runtime.json"
}
```

---

## Структура проекта

```
proxyclient/
├── cmd/proxy-client/
│   └── main.go                 — точка входа
├── internal/
│   ├── api/server.go           — HTTP API + CORS
│   ├── config/generator.go     — генерация конфига из VLESS URL
│   ├── logger/logger.go        — логгер
│   ├── proxy/manager.go        — управление системным прокси
│   ├── proxy/windows_proxy.go  — работа с реестром Windows
│   └── xray/manager.go         — запуск и остановка XRay
├── xray_core/
│   └── xray.exe                ← скачать и положить сюда
├── config.template.json        — шаблон конфигурации XRay
├── secret.key                  ← создать вручную, в git не попадёт
├── index.html                  — веб-интерфейс
└── go.mod
```

---

## Устранение неполадок

**`xray.exe не найден`** — убедитесь, что файл лежит точно по пути `xray_core/xray.exe`, а не в подпапке архива.

**`Системный прокси не включается`** — запустите `proxy-client.exe` от имени администратора.

**`Порт 8080 занят`** — другое приложение использует этот порт. Завершите его или измените `apiListenAddress` в `cmd/proxy-client/main.go`.

**`Фронтенд не видит API`** — открывайте `http://localhost:8080`, а не через `file://`. При открытии через `file://` браузер блокирует запросы из-за CORS.

**`secret.key: неверный формат`** — VLESS URL должен начинаться с `vless://` и содержать параметры `sni`, `pbk`, `sid`.