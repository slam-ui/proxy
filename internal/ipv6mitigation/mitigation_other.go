//go:build !windows

package ipv6mitigation

import (
	"context"
	"fmt"
)

func Disable(context.Context, string, string) error {
	return fmt.Errorf("IPv6 mitigation is only supported on Windows")
}

func Restore(context.Context, string) error {
	return nil
}
