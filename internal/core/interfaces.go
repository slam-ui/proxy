// Package core определяет центральные интерфейсы между слоями приложения.
//
// Цель: явные контракты между Service/Platform/API слоями без циклических зависимостей.
// Все компоненты зависят от core интерфейсов, не от конкретных реализаций.
//
// Слои зависимостей:
//
//	Platform → Support → Service → API → Application
//	                   ↑
//	               core/ (интерфейсы)
package core

import (
	"context"
	"time"
)

// XRayService управляет жизненным циклом sing-box процесса.
// Реализуется xray.manager.
type XRayService interface {
	Start() error
	// StartAfterManualCleanup запускает sing-box пропуская BeforeRestart.
	// Использовать когда вызывающая сторона уже выполнила wintun cleanup.
	StartAfterManualCleanup() error
	Stop() error
	IsRunning() bool
	GetPID() int
	Wait() error
	// LastOutput возвращает последние N байт stderr sing-box (для диагностики).
	LastOutput() string
	// Uptime возвращает время работы процесса. Ноль если не запущен.
	Uptime() time.Duration
	// GetHealthStatus возвращает метрики здоровья VLESS соединения.
	// errorCount — кол-во ошибок в скользящем окне.
	// errorRatePct — процент ошибок (0-100).
	// wouldAlert — превышен ли порог оповещения.
	GetHealthStatus() (errorCount int, errorRatePct float64, wouldAlert bool)
}

// ProxyService управляет системным HTTP-прокси Windows.
// Реализуется proxy.manager.
type ProxyService interface {
	Enable(address, override string) error
	Disable() error
	IsEnabled() bool
	GetAddress() string
	// StartGuard запускает watchdog системного прокси.
	// Восстанавливает настройки если сторонний процесс их сбросил.
	StartGuard(ctx context.Context, interval time.Duration) error
	StopGuard()
}

// RoutingService управляет правилами маршрутизации приложений.
// Реализуется apprules движком.
type RoutingService interface {
	// IsEnabled возвращает true если маршрутизация по приложениям активна.
	IsEnabled() bool
	// GetRules возвращает текущий список правил (копию).
	GetRules() []string
	// Reload перечитывает правила с диска.
	Reload() error
}

// LogService абстракция логирования.
// Реализуется logger.Logger.
type LogService interface {
	Info(format string, args ...interface{})
	Error(format string, args ...interface{})
	Debug(format string, args ...interface{})
	Warn(format string, args ...interface{})
}

// NotificationService отправляет уведомления пользователю.
// Реализуется notification пакетом.
type NotificationService interface {
	Notify(title, message string)
}
