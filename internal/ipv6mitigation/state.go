package ipv6mitigation

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"proxyclient/internal/fileutil"
)

type State struct {
	Active      bool      `json:"active"`
	Interface   string    `json:"interface"`
	DisabledAt  time.Time `json:"disabled_at,omitempty"`
	RestoreHint string    `json:"restore_hint,omitempty"`
}

func LoadState(path string) (State, error) {
	var st State
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return st, fmt.Errorf("read IPv6 mitigation state: %w", err)
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("decode IPv6 mitigation state: %w", err)
	}
	return st, nil
}

func SaveState(path string, st State) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("encode IPv6 mitigation state: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create IPv6 mitigation state dir: %w", err)
	}
	if err := fileutil.WriteAtomic(path, data, 0644); err != nil {
		return fmt.Errorf("write IPv6 mitigation state: %w", err)
	}
	return nil
}
