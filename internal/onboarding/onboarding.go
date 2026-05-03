package onboarding

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"proxyclient/internal/config"
	"proxyclient/internal/fileutil"
)

const MarkerFile = config.DataDir + "/onboarded"

type Status struct {
	Onboarded bool   `json:"onboarded"`
	Path      string `json:"path"`
}

func Current(path string) (Status, error) {
	if path == "" {
		path = MarkerFile
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Status{Onboarded: false, Path: path}, nil
		}
		return Status{}, fmt.Errorf("stat onboarding marker: %w", err)
	}
	return Status{Onboarded: true, Path: path}, nil
}

func MarkComplete(path string) error {
	if path == "" {
		path = MarkerFile
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create onboarding marker dir: %w", err)
	}
	data := []byte(time.Now().UTC().Format(time.RFC3339Nano) + "\n")
	if err := fileutil.WriteAtomic(path, data, 0644); err != nil {
		return fmt.Errorf("write onboarding marker: %w", err)
	}
	return nil
}
