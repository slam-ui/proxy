# Стратегия оптимизации топологии proxy-client

**Дата:** 2026-04-05  
**Статус:** Рекомендации для следующих фаз

---

## 1️⃣ Консолидация тестовых слоёв (ВЫСОКИЙ ПРИОРИТЕТ)

### Текущее состояние
```
api/
├─ servers_handlers.go
├─ servers_handlers_B5_test.go    ← исторический слой
├─ servers_handlers_B6_test.go    ← исторический слой
└─ servers_handlers_C5_test.go    ← текущий слой

apprules/
├─ engine.go
├─ engine_test.go                 ← базовые тесты
├─ engine_extra_test.go           ← расширенные тесты
└─ engine_extra2_test.go          ← дополнительные тесты (?)
```

### Рекомендация
**Объединить в 2 слоя:**
- `servers_handlers_test.go` — базовые + C5 (latest)
- `servers_handlers_edge_test.go` — граничные случаи (B5, B6 уникальное)

**Выигрыш:** -30% тестовых файлов, +20% читаемости


---

## 2️⃣ Разбиение больших пакетов (СРЕДНИЙ ПРИОРИТЕТ)

### 📊 Анализ размеров

| Пакет | Файлов | Рекомендация |
|-------|--------|-------------|
| **api/** | 10 src + 9 test | Разбить на подпакеты? |
| **config/** | 8-10 файлов | Разбить на `config/builder`, `config/parser` |
| **apprules/** | 7 src + 5 test | `apprules/matcher`, `apprules/storage` — уже близко |
| **xray/** | 4-5 файлов | Может вырасти → плани́ровать `xray/recovery` |

### Пример разбиения `config/`

```
config/                          # Фасад конфигурации
├─ config.go                     # Главный парсер
├─ types.go                      # Общие типы
├─ builder/
│  ├─ singbox_builder.go        # Построение sing-box конфига
│  ├─ route_builder.go          # Построение маршрутов
│  └─ builder_test.go
├─ parser/
│  ├─ vless_parser.go           # Парсинг VLESS
│  ├─ rules_parser.go           # Парсинг правил
│  └─ parser_test.go
└─ validator/
   ├─ validator.go              # Валидация конфигов
   └─ validator_test.go
```

**Выигрыш:** +clarity, -цирку́лярные зависимости


---

## 3️⃣ Явные интерфейсы между слоями (СРЕДНИЙ ПРИОРИТЕТ)

### Текущая топология

```
cmd/proxy-client/app.go
       ↓
internal/api/server.go ←→ internal/xray/manager.go
       ↓ ↓               ↓ ↓
  apprules   config    wintun  proxy
```

### Рекомендация: Слои абстракции

**Создать `internal/core/` — ядро приложения:**

```
internal/
├─ core/                         # NEW: Ядро (dependency injection)
│  ├─ app.go                     # AppContext, lifecycle
│  ├─ interfaces.go              # Экспортируемые интерфейсы
│  └─ factory.go                 # Создание компонентов
├─ api/                          # REST handlers (зависит от core)
├─ config/                       # Парсинг (зависит от core)
├─ routing/                      # Маршрутизация (зависит от core)
├─ xray/                         # sing-box (зависит от core)
└─ platform/                     # Windows-специфичное (нижний уровень)
   ├─ wintun/
   ├─ proxy/
   └─ process/
```

**Выигрыш:** Чёткие границы, тестирование проще, зависимости явные


---

## 4️⃣ Перестроить граф зависимостей (ВЫСОКИЙ ПРИОРИТЕТ)

### Текущие проблемы

```
api/server.go → apprules, xray, config  (много зависимостей)
xray/manager.go → config, wintun         (много зависимостей)
config/builder.go → apprules (?)         (возможна циклич?)
```

### Рекомендница: Инверсия управления (Dependency Injection)

**Вариант 1: Service Locator**
```go
// internal/core/services.go
type Services struct {
    Config ConfigService
    XRay   XRayService
    Rules  RoutingService
}

// cmd/proxy-client/app.go
svc := core.NewServices()
api.NewServer(svc)  // Server получает зависимости
```

**Вариант 2: Wire-генератор**
```go
// Использовать github.com/google/wire
// wire.go будет автогенерировать инициализацию
```

**Выигрыш:** Нет циклических зависимостей, mocking для тестов, гибкость


---

## 5️⃣ Организация утилит (НИЗКИЙ ПРИОРИТЕТ)

### Группировка `internal/`

```
internal/platform/              # Windows-специфичное
├─ wintun/
├─ proxy/
├─ process/
├─ notification/
└─ window/

internal/services/              # Основной функционал
├─ api/
├─ routing/
├─ config/
└─ xray/

internal/support/               # Поддерживающее
├─ logger/
├─ eventlog/
├─ anomalylog/
├─ netutil/
├─ fileutil/
├─ autorun/
└─ killswitch/
```

**Выигрыш:** Логическая группировка, проще навигировать новичкам


---

## 6️⃣ Оптимизация импортов (НИЗКИЙ ПРИОРИТЕТ)

### Проверить циклические зависимости

```bash
# Установить go-imports-analyzer
go install github.com/nickng/dingo@latest

# Визуализировать граф
dingo -f dot ./... | dot -Tpng > deps.png
```

### Результат: Найти и устранить циклические импорты


---

## 7️⃣ Документирование архитектуры (НЕМЕДЛЕННО)

### Создать `docs/ARCHITECTURE.md`

```markdown
# Архитектура proxy-client

## Слои приложения

1. **Platform Layer** — Windows API (wintun, proxy, process)
2. **Service Layer** — Config, XRay, RoutingEngine
3. **API Layer** — REST handlers, Web UI
4. **Application Layer** — Main app, lifecycle

## Правила зависимостей

- Platform → Service → API → Application (только вверх)
- API не знает о конкретной реализации Platform
- Service использует интерфейсы, не конкретные типы
```

---

## 🎯 План действий

### Фаза 1 (НЕДЕЛЯ 1)
- [ ] Консолидировать тесты `_B5/_B6/_C5`
- [ ] Документировать интерфейсы в `docs/ARCHITECTURE.md`

### Фаза 2 (НЕДЕЛЯ 2)
- [ ] Создать `internal/core/` с сервис-локатором
- [ ] Миграция зависимостей в `api/`, `xray/`

### Фаза 3 (НЕДЕЛЯ 3)
- [ ] Разбить `config/` на подпакеты (`builder/`, `parser/`)
- [ ] Проверить циклические импорты

### Фаза 4 (НЕДЕЛЯ 4)
- [ ] Перегруппировать `internal/` на `platform/`, `services/`, `support/`
- [ ] Обновить документацию

---

## 📊 Ожидаемый результат

| Метрика | До | После | Выигрыш |
|---------|----|----|--------|
| Тестовых файлов | 14 | ~10 | -30% |
| Циклических зависимостей | ? | 0 | Явные границы |
| Строк в одном файле | ~200-500 | ~150-300 | -40% |
| Новичкам на понимание | 2-3 дня | 1 день | -67% |

