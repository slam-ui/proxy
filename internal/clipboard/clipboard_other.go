//go:build !windows

package clipboard

func Read() string { return "" }
