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

// NormalizePattern нормализует паттерн один раз при сохранении правила.
// OPT #1: ранее filepath.ToSlash + ToLower вызывались при КАЖДОМ Match() —
// ~8000 аллокаций на запрос /api/apps/processes (400 процессов × 20 правил).
// Теперь нормализация выполняется один раз в AddRule/restoreRule.
func NormalizePattern(pattern string) string {
	return filepath.ToSlash(strings.ToLower(pattern))
}

// Match проверяет соответствие значения паттерну.
// Паттерн должен быть уже нормализован через NormalizePattern.
// Значение нормализуется здесь — оно приходит из ОС и не кэшируется.
func (m *matcher) Match(pattern string, value string) bool {
	// Нормализуем только value — pattern уже нормализован при сохранении правила.
	value = filepath.ToSlash(strings.ToLower(value))

	// Точное совпадение
	if pattern == value {
		return true
	}

	// Wildcard matching по полному пути
	matched, _ := filepath.Match(pattern, value)
	if matched {
		return true
	}

	// Совпадение по имени файла (basename)
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

	// OPT #9: Contains только для паттернов без wildcard-символов.
	// filepath.Match уже поймал бы "chrom*" → "chrome.exe",
	// поэтому Contains нужен лишь для точных подстрок ("chrome" → "google_chrome.exe").
	// Без проверки "chrom*" давал бы ложный Contains по символу '*'.
	if !strings.ContainsAny(pattern, "*?[") && strings.Contains(valueBase, pattern) {
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
