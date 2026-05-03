//go:build !windows

package window

var closeToTray = true

func BringToFront(string) {}
func Open(string)         {}
func Close()              {}

func SetCloseToTray(enabled bool) { closeToTray = enabled }
func CloseToTray() bool           { return closeToTray }
