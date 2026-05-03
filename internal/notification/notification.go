package notification

import (
	"sync"

	"proxyclient/internal/i18n"
)

var (
	notificationLocaleMu sync.RWMutex
	notificationLocale   = i18n.LocaleRU
)

func SetLanguage(setting string) {
	notificationLocaleMu.Lock()
	notificationLocale = i18n.EffectiveLocale(setting)
	notificationLocaleMu.Unlock()
}

func notificationT(key string, args ...any) string {
	notificationLocaleMu.RLock()
	locale := notificationLocale
	notificationLocaleMu.RUnlock()
	tr, err := i18n.New(locale)
	if err != nil {
		return key
	}
	return tr.T(key, args...)
}
