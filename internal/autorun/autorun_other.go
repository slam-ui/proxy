//go:build !windows

package autorun

func Enable() error  { return nil }
func Disable() error { return nil }
func IsEnabled() bool {
	return false
}
