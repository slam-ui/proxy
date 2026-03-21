//go:build !windows

package killswitch

import "proxyclient/internal/logger"

func IsEnabled() bool                              { return false }
func Enable(_ string, _ logger.Logger)             {}
func Disable(_ logger.Logger)                      {}
func CleanupOnStart(_ logger.Logger)               {}
