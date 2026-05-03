package errcodes

import (
	"testing"

	"proxyclient/internal/i18n"
)

func TestMessageUsesLocale(t *testing.T) {
	ru := Message(i18n.LocaleRU, RealityHandshakeFail)
	if ru.Title != "Не удалось подключиться" {
		t.Fatalf("ru title=%q", ru.Title)
	}
	en := Message(i18n.LocaleEN, RealityHandshakeFail)
	if en.Title != "Connection failed" {
		t.Fatalf("en title=%q", en.Title)
	}
}

func TestMessageFallbacksToInternalError(t *testing.T) {
	msg := Message(i18n.LocaleEN, Code("UNKNOWN"))
	if msg.Title != "Internal error" {
		t.Fatalf("fallback title=%q", msg.Title)
	}
}
