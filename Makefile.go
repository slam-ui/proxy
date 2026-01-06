.PHONY: build run clean test lint install-deps help

# Переменные
BINARY_NAME=proxy-client
MAIN_PATH=./cmd/proxy-client
BUILD_DIR=./build
GO=go
GOFLAGS=-v

# Цвета для вывода
RED=\033[0;31m
GREEN=\033[0;32m
YELLOW=\033[1;33m
NC=\033[0m # No Color

help: ## Показать справку
@echo "Доступные команды:"
@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  ${GREEN}%-15s${NC} %s\n", $$1, $$2}'

build: ## Собрать приложение
@echo "${GREEN}Сборка приложения...${NC}"
@mkdir -p $(BUILD_DIR)
$(GO) build $(GOFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME).exe $(MAIN_PATH)
@echo "${GREEN}✓ Сборка завершена: $(BUILD_DIR)/$(BINARY_NAME).exe${NC}"

build-release: ## Собрать release версию (с оптимизацией)
@echo "${GREEN}Сборка release версии...${NC}"
@mkdir -p $(BUILD_DIR)
$(GO) build -ldflags="-s -w" -o $(BUILD_DIR)/$(BINARY_NAME).exe $(MAIN_PATH)
@echo "${GREEN}✓ Release сборка завершена${NC}"

run: ## Запустить приложение
@echo "${GREEN}Запуск приложения...${NC}"
$(GO) run $(MAIN_PATH)/main.go

clean: ## Очистить артефакты сборки
@echo "${YELLOW}Очистка...${NC}"
@rm -rf $(BUILD_DIR)
@rm -f config.runtime.json
@echo "${GREEN}✓ Очистка завершена${NC}"

test: ## Запустить тесты
@echo "${GREEN}Запуск тестов...${NC}"
$(GO) test -v ./...

test-coverage: ## Запустить тесты с покрытием
@echo "${GREEN}Запуск тестов с покрытием...${NC}"
$(GO) test -v -coverprofile=coverage.out ./...
$(GO) tool cover -html=coverage.out -o coverage.html
@echo "${GREEN}✓ Отчет о покрытии: coverage.html${NC}"

lint: ## Запустить линтер
@echo "${GREEN}Запуск линтера...${NC}"
@which golangci-lint > /dev/null || (echo "${RED}golangci-lint не установлен${NC}" && exit 1)
golangci-lint run ./...

fmt: ## Форматировать код
@echo "${GREEN}Форматирование кода...${NC}"
$(GO) fmt ./...
@echo "${GREEN}✓ Форматирование завершено${NC}"

install-deps: ## Установить зависимости
@echo "${GREEN}Установка зависимостей...${NC}"
$(GO) mod download
$(GO) mod tidy
@echo "${GREEN}✓ Зависимости установлены${NC}"

verify: ## Проверить код (fmt, lint, test)
@echo "${GREEN}Полная проверка кода...${NC}"
@$(MAKE) fmt
@$(MAKE) lint
@$(MAKE) test
@echo "${GREEN}✓ Все проверки пройдены${NC}"

deps-update: ## Обновить зависимости
@echo "${GREEN}Обновление зависимостей...${NC}"
$(GO) get -u ./...
$(GO) mod tidy
@echo "${GREEN}✓ Зависимости обновлены${NC}"

check-secret: ## Проверить наличие secret.key
@if [ ! -f secret.key ]; then \
echo "${RED}Ошибка: файл secret.key не найден${NC}"; \
echo "${YELLOW}Создайте файл secret.key с VLESS URL${NC}"; \
exit 1; \
else \
echo "${GREEN}✓ Файл secret.key найден${NC}"; \
fi

check-xray: ## Проверить наличие XRay
@if [ ! -f ./xray_core/xray.exe ]; then \
echo "${RED}Ошибка: XRay не найден в ./xray_core/xray.exe${NC}"; \
echo "${YELLOW}Скачайте XRay Core и поместите в ./xray_core/${NC}"; \
exit 1; \
else \
echo "${GREEN}✓ XRay найден${NC}"; \
fi

setup: install-deps check-secret check-xray ## Полная настройка проекта
@echo "${GREEN}✓ Проект настроен и готов к работе${NC}"

info: ## Показать информацию о проекте
@echo "${GREEN}=== Информация о проекте ===${NC}"
@echo "Go версия: $(shell $(GO) version)"
@echo "Модуль: $(shell head -1 go.mod | cut -d' ' -f2)"
@echo "Бинарник: $(BUILD_DIR)/$(BINARY_NAME).exe"
@echo ""
@echo "${GREEN}=== Файлы конфигурации ===${NC}"
@ls -lh config.template.json 2>/dev/null || echo "${YELLOW}config.template.json не найден${NC}"
@ls -lh secret.key 2>/dev/null || echo "${YELLOW}secret.key не найден${NC}"
@ls -lh config.runtime.json 2>/dev/null || echo "${YELLOW}config.runtime.json еще не создан${NC}"