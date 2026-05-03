package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"
)

type Locale string

const (
	LocaleRU Locale = "ru"
	LocaleEN Locale = "en"
)

//go:embed locales/*.json
var localeFiles embed.FS

type Translator struct {
	current Locale
	msgs    map[Locale]map[string]string
}

func New(locale Locale) (*Translator, error) {
	msgs, err := LoadMessages()
	if err != nil {
		return nil, err
	}
	return &Translator{current: NormalizeLocale(locale), msgs: msgs}, nil
}

func LoadMessages() (map[Locale]map[string]string, error) {
	out := map[Locale]map[string]string{}
	for _, loc := range []Locale{LocaleRU, LocaleEN} {
		data, err := localeFiles.ReadFile("locales/" + string(loc) + ".json")
		if err != nil {
			return nil, fmt.Errorf("read locale %s: %w", loc, err)
		}
		msgs := map[string]string{}
		if err := json.Unmarshal(data, &msgs); err != nil {
			return nil, fmt.Errorf("parse locale %s: %w", loc, err)
		}
		out[loc] = msgs
	}
	return out, nil
}

func NormalizeLocale(locale Locale) Locale {
	raw := strings.ToLower(strings.TrimSpace(string(locale)))
	if strings.HasPrefix(raw, "ru") {
		return LocaleRU
	}
	if strings.HasPrefix(raw, "en") {
		return LocaleEN
	}
	return LocaleEN
}

func EffectiveLocale(setting string) Locale {
	if strings.EqualFold(strings.TrimSpace(setting), "system") || strings.TrimSpace(setting) == "" {
		return SystemLocale()
	}
	return NormalizeLocale(Locale(setting))
}

func (t *Translator) T(key string, args ...any) string {
	if t == nil {
		return key
	}
	msg := ""
	if byLocale := t.msgs[t.current]; byLocale != nil {
		msg = byLocale[key]
	}
	if msg == "" {
		msg = t.msgs[LocaleEN][key]
	}
	if msg == "" {
		msg = t.msgs[LocaleRU][key]
	}
	if msg == "" {
		msg = key
	}
	if len(args) == 0 {
		return msg
	}
	return fmt.Sprintf(msg, args...)
}

func Plural(locale Locale, one, few, many string, n int) string {
	switch NormalizeLocale(locale) {
	case LocaleRU:
		mod10 := n % 10
		mod100 := n % 100
		if mod10 == 1 && mod100 != 11 {
			return one
		}
		if mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14) {
			return few
		}
		return many
	default:
		if n == 1 {
			return one
		}
		return many
	}
}
