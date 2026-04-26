//go:build !windows

package main

import (
	"fmt"
	"runtime"
)

func main() {
	fmt.Printf("SafeSky proxy-client supports Windows only; current platform is %s/%s\n", runtime.GOOS, runtime.GOARCH)
}
