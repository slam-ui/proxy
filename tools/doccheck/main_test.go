package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckMarkdownFileReportsMissingLocalLink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.md")
	if err := os.WriteFile(path, []byte("[missing](missing.md)\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	problems := checkMarkdownFile(path)
	if len(problems) != 1 {
		t.Fatalf("problems = %v, want one missing link", problems)
	}
}

func TestCheckMarkdownFileAcceptsMermaidBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.md")
	content := "```mermaid\nflowchart LR\n  A --> B\n```\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if problems := checkMarkdownFile(path); len(problems) != 0 {
		t.Fatalf("problems = %v, want none", problems)
	}
}

func TestCheckPackageDocRequiresPackageComment(t *testing.T) {
	dir := t.TempDir()
	pkg := filepath.Join(dir, "pkg")
	if err := os.MkdirAll(pkg, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "pkg.go"), []byte("package pkg\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if got := checkPackageDoc(pkg); got == "" {
		t.Fatal("checkPackageDoc accepted package without comment")
	}
}
