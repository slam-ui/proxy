package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCopyWithRetryReplacesFileAndCreatesBackupSource(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "new.exe")
	dst := filepath.Join(dir, "SafeSky.exe")
	if err := os.WriteFile(src, []byte("new"), 0644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	if err := os.WriteFile(dst, []byte("old"), 0644); err != nil {
		t.Fatalf("write dst: %v", err)
	}
	if err := copyWithRetry(dst, dst+".bak"); err != nil {
		t.Fatalf("backup: %v", err)
	}
	if err := copyWithRetry(src, dst); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != "new" {
		t.Fatalf("dst = %q, want new", got)
	}
	bak, err := os.ReadFile(dst + ".bak")
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(bak) != "old" {
		t.Fatalf("backup = %q, want old", bak)
	}
}

func TestSplitArgs(t *testing.T) {
	got := splitArgs(" --no-update  --debug ")
	want := []string{"--no-update", "--debug"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitArgs = %#v, want %#v", got, want)
	}
}

func TestRunRequiresPaths(t *testing.T) {
	if err := run(nil); err == nil {
		t.Fatal("run succeeded without paths")
	}
}
