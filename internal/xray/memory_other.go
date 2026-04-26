//go:build !windows

package xray

func (m *manager) MemoryMB() uint64 { return 0 }
