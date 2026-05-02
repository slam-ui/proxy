package update

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"proxyclient/internal/fileutil"
)

// State tracks staged or recently installed updates.
type State struct {
	PendingPath string    `json:"pending_path,omitempty"`
	BackupPath  string    `json:"backup_path,omitempty"`
	InstalledAt time.Time `json:"installed_at,omitempty"`
	Verified    bool      `json:"verified"`
}

// LoadState reads update_state.json. Missing state is not an error.
func LoadState(path string) (State, error) {
	var st State
	if path == "" {
		return st, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return st, fmt.Errorf("read update state: %w", err)
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("decode update state: %w", err)
	}
	return st, nil
}

// SaveState writes update_state.json atomically.
func SaveState(path string, st State) error {
	if path == "" {
		return nil
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("encode update state: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create update state dir: %w", err)
	}
	if err := fileutil.WriteAtomic(path, data, 0644); err != nil {
		return fmt.Errorf("write update state: %w", err)
	}
	return nil
}

// MarkVerified marks a newly installed version as healthy.
func MarkVerified(path string) error {
	st, err := LoadState(path)
	if err != nil {
		return err
	}
	st.PendingPath = ""
	st.Verified = true
	return SaveState(path, st)
}
