//go:build !windows

package i18n

import (
	"os"
	"strings"
)

func SystemLocale() Locale {
	raw := strings.ToLower(os.Getenv("LANG"))
	if strings.HasPrefix(raw, "ru") {
		return LocaleRU
	}
	return LocaleEN
}
