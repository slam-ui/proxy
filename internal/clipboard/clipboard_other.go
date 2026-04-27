//go:build !windows

package clipboard

func Read() string { return "" }

func Write(string) bool { return false }
