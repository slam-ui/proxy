//go:build !windows

package netwatch

import "context"

func Watch(ctx context.Context, onChange func()) error {
	<-ctx.Done()
	return nil
}
