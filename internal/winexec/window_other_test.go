//go:build !windows

package winexec

import (
	"os/exec"
	"testing"
)

func TestHideWindowNoopOnOtherPlatforms(t *testing.T) {
	HideWindow(exec.Command("true"))
}
