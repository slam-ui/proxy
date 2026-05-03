package main

import (
	"bufio"
	"flag"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var markdownLinkRE = regexp.MustCompile(`\[[^\]]+\]\(([^)]+)\)`)

func main() {
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	if err := run(*root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(root string) error {
	var problems []string
	problems = append(problems, checkMarkdown(root)...)
	problems = append(problems, checkPackageDocs(filepath.Join(root, "internal"))...)
	if len(problems) > 0 {
		return fmt.Errorf("doccheck failed:\n%s", strings.Join(problems, "\n"))
	}
	return nil
}

func checkMarkdown(root string) []string {
	var problems []string
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", path, err))
			return nil
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == ".audit" || entry.Name() == ".claude" || entry.Name() == "dist" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.EqualFold(filepath.Ext(path), ".md") {
			problems = append(problems, checkMarkdownFile(path)...)
		}
		return nil
	})
	return problems
}

func checkMarkdownFile(path string) []string {
	file, err := os.Open(path)
	if err != nil {
		return []string{fmt.Sprintf("%s: %v", path, err)}
	}
	defer file.Close()

	var problems []string
	inMermaid := false
	mermaidStart := 0
	mermaidLines := 0
	scanner := bufio.NewScanner(file)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if inMermaid {
				if mermaidLines == 0 {
					problems = append(problems, fmt.Sprintf("%s:%d: empty mermaid block", path, mermaidStart))
				}
				inMermaid = false
				mermaidStart = 0
				mermaidLines = 0
				continue
			}
			if trimmed == "```mermaid" {
				inMermaid = true
				mermaidStart = lineNo
				continue
			}
		}
		if inMermaid && trimmed != "" {
			mermaidLines++
		}
		for _, match := range markdownLinkRE.FindAllStringSubmatch(line, -1) {
			target := strings.TrimSpace(match[1])
			if problem := checkLocalMarkdownLink(path, lineNo, target); problem != "" {
				problems = append(problems, problem)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		problems = append(problems, fmt.Sprintf("%s: %v", path, err))
	}
	if inMermaid {
		problems = append(problems, fmt.Sprintf("%s:%d: unclosed mermaid block", path, mermaidStart))
	}
	return problems
}

func checkLocalMarkdownLink(path string, lineNo int, target string) string {
	if target == "" || strings.HasPrefix(target, "#") ||
		strings.HasPrefix(target, "http://") ||
		strings.HasPrefix(target, "https://") ||
		strings.HasPrefix(target, "mailto:") {
		return ""
	}
	target = strings.Split(target, "#")[0]
	if target == "" {
		return ""
	}
	clean := filepath.Clean(filepath.Join(filepath.Dir(path), filepath.FromSlash(target)))
	if _, err := os.Stat(clean); err != nil {
		return fmt.Sprintf("%s:%d: missing link target %q", path, lineNo, target)
	}
	return ""
}

func checkPackageDocs(root string) []string {
	var problems []string
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", path, err))
			return nil
		}
		if !entry.IsDir() {
			return nil
		}
		if path == root {
			return nil
		}
		if hasGoFiles(path) {
			if problem := checkPackageDoc(path); problem != "" {
				problems = append(problems, problem)
			}
		}
		return nil
	})
	return problems
}

func hasGoFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasSuffix(name, "_test.go") || !strings.HasSuffix(name, ".go") {
			continue
		}
		return true
	}
	return false
}

func checkPackageDoc(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Sprintf("%s: %v", dir, err)
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasSuffix(name, "_test.go") || !strings.HasSuffix(name, ".go") {
			continue
		}
		path := filepath.Join(dir, name)
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.PackageClauseOnly)
		if err != nil {
			return fmt.Sprintf("%s: %v", path, err)
		}
		if file.Doc == nil {
			continue
		}
		doc := strings.TrimSpace(file.Doc.Text())
		if strings.HasPrefix(doc, "Package "+file.Name.Name+" ") ||
			strings.HasPrefix(doc, "Package "+file.Name.Name+".") {
			return ""
		}
	}
	return fmt.Sprintf("%s: missing package comment", dir)
}
