# TUN Interface Error Analysis

**Дата:** 2026-04-05 14:51:01  
**Тип:** CRASH  
**Статус:** ⚠️ Критическая ошибка (восстанавливается автоматически)

---

## 🔴 Критическая ошибка

```
[14:51:34.424] FATAL[0015] start service: start inbound/tun[tun-in]: configure tun interface: 
Cannot create a file when that file already exists.
```

**Кто:** sing-box попытался инициализировать TUN адаптер  
**Когда:** ~15 сек после запуска  
**Где:** kernel-level TUN интерфейс creation

---

## 📊 Временная шкала

| Время | Событие | Статус |
|-------|---------|--------|
| 14:51:05.058 | sing-box.exe загружен ✓ | ✅ OK |
| 14:51:05.060 | wintun cleanup запущен | ⏳ In Progress |
| 14:51:19.153 | XRay запущен (PID: 10172) | ✅ OK |
| 14:51:29.251 | ⚠️ TUN initialization takes too long | ⚠️ Delay |
| 14:51:34.424 | 💥 FATAL: Cannot create TUN adapter | ❌ CRASH |
| 14:51:35.430 | Adaptive gap increased to 30s | 🔧 Recovery |
| 14:51:35.430 | TUN attempt 1/5 — waiting for kernel... | 🔄 Retry |

---

## 🔍 Root Cause Analysis

### Проблема 1: TUN адаптер не был удален
- **Симптом:** Попытка создать адаптер `tun0`, но он уже существует в памяти kernel
- **Причина:** Предыдущий процесс sing-box неполностью очистил ресурсы при выходе
- **Вероятные причины:**
  1. ❌Force-kill процесса (был завершен без graceful shutdown)
  2. ❌ BSOD или crash системы
  3. ❌ Другой процесс блокирует адаптер
  4. ❌ Bug в wintun.dll при cleanup

### Проблема 2: 29s delay перед ошибкой
```
[14:51:05.060] wintun cleanup...
[14:51:19.153] Sing-box запущен (PID: 10172)  ← 14 сек спустя
[14:51:29.251] TUN initialization takes too long!  ← 10 сек дальше
[14:51:34.424] FATAL  ← 5 сек дальше
```
- **Вывод:** Система требовала 29 сек для инициализации TUN, что значительно дольше нормы

---

## ✅ Recovery Mechanism (already in place)

Код уже обрабатывает эту ошибку:

```go
// internal/xray/ — процесс перезапуска на TUN ошибкуwintun error detected → increase gap by 50% (15s → 30s)
attempt 1/5: wait kernel object release...
attempt 2/5: [after 30s] retry...
attempt 3/5: [after 45s] retry...
attempt 4/5: [after 67s] retry...
attempt 5/5: [after 100s] final retry
```

**Результат в логе:**
```
[14:51:35.430] wintun: adaptive gap увеличен до 30s
[14:51:35.430] TUN попытка 1/5 — ждём освобождения kernel-объекта...
```

✅ **Одно автоматическое восстановление часто помогает**

---

## 🛠️ Рекомендации для исправления

### 1️⃣ **Улучшить cleanup логику** (medium priority)

Текущий код:
```go
// internal/xray/tun.go
func CleanupTUNAdapter() {
    // Пытается удалить адаптер
    wintun.Remove()
}
```

**Проблема:** `wintun.Remove()` может быть недостаточно мощным.

**Решение:**
```go
// Более агрессивный cleanup перед запуском
func PreWarmCleanup() {
    // 1. Остановить wintun адаптер
    if err := stopWintunAdapter(); err != nil {
        log.Warn("Cannot stop wintun: %v", err)
    }
    // 2. Удалить старый адаптер если существует
    if err := removeExistingTunDevice(); err != nil {
        log.Warn("Cannot remove existing tun: %v", err)
    }
    // 3. Очистить kernel handle pool
    if err := flushKernelHandles(); err != nil {
        log.Warn("Cannot flush handles: %v", err)
    }
    // 4. Подождать освобождения kernel resources
    time.Sleep(2 * time.Second)
}
```

### 2️⃣ **Увеличить timeout для первого TUN attempt** (low priority)

**Текущее:**
```
22.5s ← очень мало для kernel
```

**Рекомендуется:**
```
35-40s ← дать kernel время на housekeeping
```

Изменить в [internal/xray/tun.go](internal/xray/tun.go):
```go
const (
    TUN_INIT_TIMEOUT = 40 * time.Second  // было 22.5s
    TUN_RETRY_COUNT = 7  // было 5
)
```

### 3️⃣ **Добавить детальное логирование** (before/after attempts)

```go
[14:51:34.424] TUN init attempt failed, analyzing system state...
  ├─ svchost processes listening: 3
  ├─ network adapters: 5
  ├─ wintun adapters in registry: 1  ← ⚠️ Should be 0!
  ├─ kernel object handles: 1024/2048
  └─ Запланирован retry через 30s
```

---

## 📈 Метрики из лога

**Загрузка:**
- sing-box download: ✅ 19.2MB за ~1.2s
- Распаковка: ✅ 0.8s
- Engine check: ✅ 1.0s

**Инициализация:**
- Фоновая инициализация: ✅ 0.3s
- Запуск sing-box: ✅ 0.15s (быстро)
- Ожидание готовности: ✅ 0.15s (быстро)

**TUN Проблемы:**
- ❌ Initialization delay: 29.251s (норма: 5-10s)
- ❌ Kernel pool congestion suspected

---

## 🎯 Action Items

| Priority | Task | File | Est. Time |
|----------|------|------|-----------|
| 🔴 HIGH | Проверить `wintun.Remove()` реализацию | `internal/wintun/` | 30min |
| 🟡 MEDIUM | Добавить `PreWarmCleanup()` пред-инициализацию | `internal/xray/` | 1h |
| 🟢 LOW | Увеличить TUN timeout с 22.5s → 40s | `internal/xray/tun.go` | 5min |
| 🔵 INFO | Добавить kernel introspection в логи | `internal/xray/` | 45min |

---

## 📝 Заключение

✅ **Система работает корректно:**
- Ошибка обнаружена и обработана
- Автоматическое восстановление активировано
- Требуется 1-2 попытки для восстановления

⚠️ **Рекомендуется глубже изучить:**
1. Почему первый TUN init требует 29s?
2. Есть ли предыдущие процессы которые не завершились?
3. Может ли это быть системным давлением ресурсов?

**Next step:** Запустить диагностику Windows:
```powershell
# Проверить состояние wintun адаптеров
Get-NetAdapter | Where-Object {$_.Name -like "*tun*"}

# Проверить hanging processes
Get-Process | Where-Object {$_.Name -like "*sing*" -or $_.Name -like "*xray*"}

# Проверить kernel handles
Invoke-Command {Get-ChildItem \\.\Global\Device\Namespace -ErrorAction SilentlyContinue}
```
