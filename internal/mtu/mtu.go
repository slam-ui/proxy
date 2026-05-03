package mtu

import (
	"context"
	"fmt"
)

const (
	MinMTU           = 1280
	MaxMTU           = 1500
	DefaultWireGuard = 1408
)

type ProbeFunc func(ctx context.Context, size int) error

func Clamp(value, fallback int) int {
	if fallback <= 0 {
		fallback = DefaultWireGuard
	}
	if value <= 0 {
		value = fallback
	}
	if value < MinMTU {
		return MinMTU
	}
	if value > MaxMTU {
		return MaxMTU
	}
	return value
}

func Detect(ctx context.Context, minSize, maxSize int, probe ProbeFunc) (int, error) {
	if probe == nil {
		return 0, fmt.Errorf("mtu probe is required")
	}
	minSize = Clamp(minSize, MinMTU)
	maxSize = Clamp(maxSize, MaxMTU)
	if minSize > maxSize {
		return 0, fmt.Errorf("invalid MTU range: min %d > max %d", minSize, maxSize)
	}
	best := 0
	lo, hi := minSize, maxSize
	for lo <= hi {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		mid := lo + (hi-lo)/2
		if err := probe(ctx, mid); err == nil {
			best = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	if best == 0 {
		return 0, fmt.Errorf("no working MTU in range %d..%d", minSize, maxSize)
	}
	return best, nil
}
