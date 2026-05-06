package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsOnlyFilesHaveBuildTagsAndOtherStubs(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == "dist" {
				return filepath.SkipDir
			}
			return nil
		}

		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		first := firstLines(string(data), 3)
		if !strings.Contains(first, "//go:build windows") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}

		stub, ok := findOtherStub(path)
		if !ok {
			t.Errorf("%s: missing non-Windows stub", path)
			return nil
		}
		stubData, readErr := os.ReadFile(stub)
		if readErr != nil {
			return readErr
		}
		if !strings.Contains(firstLines(string(stubData), 3), "//go:build !windows") {
			t.Errorf("%s: missing //go:build !windows in first lines", stub)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func findOtherStub(path string) (string, bool) {
	candidates := []string{}
	switch {
	case strings.HasSuffix(path, "_windows.go"):
		candidates = append(candidates, strings.TrimSuffix(path, "_windows.go")+"_other.go")
	case strings.HasSuffix(path, "_win32.go"):
		candidates = append(candidates, strings.TrimSuffix(path, "_win32.go")+"_other.go")
	}
	candidates = append(candidates, strings.TrimSuffix(path, ".go")+"_other.go")
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true
		}
	}
	return "", false
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) < n {
		n = len(lines)
	}
	return strings.Join(lines[:n], "\n")
}
