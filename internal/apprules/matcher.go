package apprules

import (
	"path/filepath"
	"strings"
)

// Matcher интерфейс для сопоставления правил
type Matcher interface {
	Match(pattern string, value string) bool
}

// matcher реализация Matcher
type matcher struct{}

// NewMatcher создает новый matcher
func NewMatcher() Matcher {
	return &matcher{}
}

// Match проверяет соответствие значения паттерну
func (m *matcher) Match(pattern string, value string) bool {
	// Нормализуем пути
	pattern = filepath.ToSlash(strings.ToLower(pattern))
	value = filepath.ToSlash(strings.ToLower(value))

	// Точное совпадение
	if pattern == value {
		return true
	}

	// Wildcard matching
	matched, _ := filepath.Match(pattern, value)
	if matched {
		return true
	}

	// Совпадение по имени файла
	valueBase := filepath.Base(value)
	patternBase := filepath.Base(pattern)

	if patternBase == valueBase {
		return true
	}

	// Wildcard по имени файла
	matched, _ = filepath.Match(patternBase, valueBase)
	if matched {
		return true
	}

	// Contains matching для частичного совпадения
	if strings.Contains(value, pattern) {
		return true
	}

	return false
}

// MatchAny проверяет соответствие хотя бы одному паттерну
func MatchAny(matcher Matcher, patterns []string, value string) bool {
	for _, pattern := range patterns {
		if matcher.Match(pattern, value) {
			return true
		}
	}
	return false
}
