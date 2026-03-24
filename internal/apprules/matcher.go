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

// matchWildcard проверяет wildcard-паттерн против строки.
// Использует собственную реализацию вместо filepath.Match/path.Match потому что:
// - filepath.Match на Windows использует '\' как разделитель — "chrome*" не матчит "chromium.exe"
// - path.Match использует '/' но '*' не матчит через '/' — "c:/prog*" не матчит "c:/program files/app.exe"
// Наша реализация: '*' матчит любую последовательность символов включая '/'.
func matchWildcard(pattern, s string) bool {
	// Быстрый путь без wildcard
	if !strings.ContainsAny(pattern, "*?") {
		return pattern == s
	}
	return wildcardMatch(pattern, s)
}

// wildcardMatch рекурсивный wildcard matching где '*' матчит всё.
func wildcardMatch(pattern, s string) bool {
	for len(pattern) > 0 {
		switch pattern[0] {
		case '*':
			// Пропускаем повторные '*'
			for len(pattern) > 0 && pattern[0] == '*' {
				pattern = pattern[1:]
			}
			if len(pattern) == 0 {
				return true
			}
			// Пробуем матчить остаток паттерна с каждой позиции в s
			for i := 0; i <= len(s); i++ {
				if wildcardMatch(pattern, s[i:]) {
					return true
				}
			}
			return false
		case '?':
			if len(s) == 0 {
				return false
			}
			pattern = pattern[1:]
			s = s[1:]
		default:
			if len(s) == 0 || pattern[0] != s[0] {
				return false
			}
			pattern = pattern[1:]
			s = s[1:]
		}
	}
	return len(s) == 0
}

// Match проверяет соответствие значения паттерну.
// Паттерн должен быть уже нормализован через NormalizePattern.
// Значение нормализуется здесь — оно приходит из ОС и не кэшируется.
func (m *matcher) Match(pattern string, value string) bool {
	normPattern := NormalizePattern(pattern)
	normValue := filepath.ToSlash(strings.ToLower(value))

	// Точное побайтовое совпадение (до любой нормализации).
	if pattern == value {
		return true
	}

	// Case-insensitive совпадение только для ASCII/валидного UTF-8.
	// BUG FIX: невалидные UTF-8 байты \xde и \xda → одинаковый U+FFFD после ToLower,
	// поэтому normPattern == normValue для разных байт — ложный матч.
	// Защита: разрешаем нормализованное совпадение только если паттерн состоит из ASCII.
	if normPattern == normValue {
		patternIsASCII := true
		for i := 0; i < len(pattern); i++ {
			if pattern[i] > 127 {
				patternIsASCII = false
				break
			}
		}
		if patternIsASCII {
			return true
		}
	}

	// BUG FIX #2: собственный wildcardMatch вместо filepath.Match/path.Match.
	// filepath.Match на Windows: '*' не матчит через компоненты пути.
	// path.Match: '*' не матчит через '/'.
	// wildcardMatch: '*' матчит всё включая '/'.
	if matchWildcard(normPattern, normValue) {
		return true
	}

	// Совпадение по имени файла (basename)
	valueBase := filepath.Base(normValue)
	patternBase := filepath.Base(normPattern)

	if patternBase == valueBase {
		return true
	}

	if matchWildcard(patternBase, valueBase) {
		return true
	}

	// OPT #9: Contains только для паттернов без wildcard-символов.
	if !strings.ContainsAny(normPattern, "*?[") && strings.Contains(valueBase, normPattern) {
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
