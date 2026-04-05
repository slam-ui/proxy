//go:build !windows

package notification

// Send — заглушка для не-Windows платформ.
// На Windows реализация в notification_windows.go.
func Send(title, message string) {}
