//go:build !windows

package wintun

// kernelObjectFree — заглушка для non-Windows платформ.
// На Linux/macOS wintun.dll не существует; CI тесты запускаются без неё.
// Возвращает true (fail-open): если не Windows — не блокируем запуск.
func kernelObjectFree(_ string) bool {
	return true
}
