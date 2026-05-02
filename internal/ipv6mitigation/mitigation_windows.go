//go:build windows

package ipv6mitigation

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

func Disable(ctx context.Context, statePath, iface string) error {
	if iface == "" {
		return fmt.Errorf("interface is required")
	}
	if err := runNetsh(ctx, "interface", "ipv6", "set", "interface", iface, "disabled"); err != nil {
		return err
	}
	return SaveState(statePath, State{
		Active:      true,
		Interface:   iface,
		DisabledAt:  time.Now().UTC(),
		RestoreHint: "netsh interface ipv6 set interface " + iface + " enabled",
	})
}

func Restore(ctx context.Context, statePath string) error {
	st, err := LoadState(statePath)
	if err != nil {
		return err
	}
	if !st.Active || st.Interface == "" {
		return nil
	}
	if err := runNetsh(ctx, "interface", "ipv6", "set", "interface", st.Interface, "enabled"); err != nil {
		return err
	}
	st.Active = false
	return SaveState(statePath, st)
}

func runNetsh(parent context.Context, args ...string) error {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "netsh", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("netsh %v: %w: %s", args, err, out)
	}
	return nil
}
