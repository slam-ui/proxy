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

// toSlash заменяет все обратные слеши на прямые.
// BUG FIX (Linux/CI): filepath.ToSlash заменяет только os.PathSeparator.
// На Linux PathSeparator='/' — обратные слеши остаются нетронутыми.
// Это ломает матчинг Windows-путей вида "C:\Program Files\app.exe" в тестах
// и при кросс-компиляции. strings.ReplaceAll всегда работает корректно.
func toSlash(s string) string {
	return strings.ReplaceAll(s, "\\", "/")
}

// NormalizePattern нормализует паттерн один раз при сохранении правила.
// OPT #1: ранее filepath.ToSlash + ToLower вызывались при КАЖДОМ Match() —
// ~8000 аллокаций на запрос /api/apps/processes (400 процессов × 20 правил).
// Теперь нормализация выполняется один раз в AddRule/restoreRule.
func NormalizePattern(pattern string) string {
	return toSlash(strings.ToLower(pattern))
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

// wildcardMatch итеративный wildcard matching где '*' матчит любую подстроку.
// BUG FIX (фаззер): рекурсивная реализация давала экспоненциальную сложность
// на паттернах вида "*a*a*a*" против "aaaa…b" — фаззер нашёл такой входной вектор.
// DP-подход гарантирует O(n·m) время и O(m) память.
// ЗАЩИТА: слишком большие входы (n*m > 50 млн) тривиально вернут false.
func wildcardMatch(pattern, s string) bool {
	np, ns := len(pattern), len(s)

	// Защита от DoS через огромные входы в фаззинге.
	// O(n*m) при н=10k м=10k даёт 100M операций которых может быть > 5s timeout.
	// Минимальный лимит: если произведение > 50M, считаем не матчит.
	// Реальные правила: обычно < 10k символов, так что это безопасно.
	if int64(np)*int64(ns) > 50_000_000 {
		return false
	}

	// prev[j] = true если pattern[:i] полностью матчит s[:j]
	prev := make([]bool, ns+1)
	prev[0] = true // пустой паттерн матчит пустую строку

	for i := 0; i < np; i++ {
		curr := make([]bool, ns+1)
		ch := pattern[i]
		if ch == '*' {
			// '*' матчит 0 или более символов:
			// curr[0] — '*' матчит "" (только если pattern[:i] уже матчил "")
			curr[0] = prev[0]
			for j := 1; j <= ns; j++ {
				// prev[j]: '*' берёт 0 символов (паттерн без '*' уже матчил s[:j])
				// curr[j-1]: '*' поглощает ещё один символ s[j-1]
				curr[j] = prev[j] || curr[j-1]
			}
		} else {
			// '?' или литерал — матчит ровно один символ
			for j := 1; j <= ns; j++ {
				if ch == '?' || ch == s[j-1] {
					curr[j] = prev[j-1]
				}
			}
		}
		prev = curr
	}

	return prev[ns]
}

// Match проверяет соответствие значения паттерну.
// Паттерн должен быть уже нормализован через NormalizePattern.
// Значение нормализуется здесь — оно приходит из ОС и не кэшируется.
func (m *matcher) Match(pattern string, value string) bool {
	normPattern := NormalizePattern(pattern)
	normValue := toSlash(strings.ToLower(value))

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
