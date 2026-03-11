# Proxy Client — содержимое папки dist/

После сборки (`.\build.ps1 -Release`) папка `dist/` содержит всё необходимое
для запуска. Скопируйте её на любой Windows-компьютер — Go-рантайм не нужен.

## Структура dist/

```
dist/
├── proxy-client.exe        ← основное приложение (собирается автоматически)
├── sing-box.exe            ← ядро туннеля (скачать отдельно, см. ниже)
├── secret.key              ← ваш VLESS-ключ (заполнить вручную)
├── routing.json            ← правила маршрутизации (редактируется через UI)
├── geosite-discord.bin     ┐
├── geosite-instagram.bin   │
├── geosite-reddit.bin      │  гео-базы для geosite-правил
├── geosite-soundcloud.bin  │  (копируются автоматически)
├── geosite-spotify.bin     │
├── geosite-tiktok.bin      │
└── geosite-youtube.bin     ┘
```

Файлы, создаваемые автоматически при первом запуске (не трогайте вручную):
```
├── config.singbox.json     ← генерируется из secret.key + routing.json
├── app_rules.json          ← правила per-app proxy (через UI)
└── proxy-client.log        ← лог-файл
```

---

## Шаг 1 — Скачать sing-box

Перейдите на страницу релизов:
https://github.com/SagerNet/sing-box/releases

Скачайте файл вида `sing-box-*-windows-amd64.zip`, распакуйте и положите
`sing-box.exe` в папку `dist/`.

## Шаг 2 — Добавить VLESS-ключ

Откройте `dist/secret.key` в любом текстовом редакторе, удалите комментарии
и вставьте вашу ссылку в формате:

```
vless://UUID@HOST:PORT?sni=SNI&pbk=PUBKEY&sid=SHORTID&flow=xtls-rprx-vision
```

## Шаг 3 — Запустить

Дважды кликните `proxy-client.exe` или запустите из PowerShell:

```powershell
.\dist\proxy-client.exe
```

В трее появится иконка. Нажмите «Открыть панель» для управления правилами.

---

## Сборка

```powershell
# Debug-сборка (с консольным окном)
.\build.ps1 -NoGui

# Release-сборка (без консоли, оптимизированная)
.\build.ps1 -Release

# Пересборка с нуля
.\build.ps1 -Release -Clean
```
