package i18n

import "testing"

func TestTranslatorFallbackAndFormatting(t *testing.T) {
	tr, err := New(LocaleRU)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := tr.T("connect.error", "boom"); got != "Ошибка подключения: boom" {
		t.Fatalf("translated=%q", got)
	}
	if got := tr.T("missing.key"); got != "missing.key" {
		t.Fatalf("missing fallback=%q", got)
	}
}

func TestNormalizeLocale(t *testing.T) {
	if NormalizeLocale("ru-RU") != LocaleRU {
		t.Fatal("ru-RU should normalize to ru")
	}
	if NormalizeLocale("de-DE") != LocaleEN {
		t.Fatal("non-ru locale should normalize to en")
	}
}

func TestEffectiveLocale(t *testing.T) {
	if EffectiveLocale("ru") != LocaleRU {
		t.Fatal("ru should be effective ru")
	}
	if EffectiveLocale("en") != LocaleEN {
		t.Fatal("en should be effective en")
	}
}

func TestLocaleFilesHaveSameKeys(t *testing.T) {
	messages, err := LoadMessages()
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	ru := messages[LocaleRU]
	en := messages[LocaleEN]
	for key := range ru {
		if _, ok := en[key]; !ok {
			t.Fatalf("en locale missing key %q", key)
		}
	}
	for key := range en {
		if _, ok := ru[key]; !ok {
			t.Fatalf("ru locale missing key %q", key)
		}
	}
}

func TestPlural(t *testing.T) {
	if Plural(LocaleRU, "one", "few", "many", 1) != "one" {
		t.Fatal("ru 1 should use one")
	}
	if Plural(LocaleRU, "one", "few", "many", 3) != "few" {
		t.Fatal("ru 3 should use few")
	}
	if Plural(LocaleRU, "one", "few", "many", 11) != "many" {
		t.Fatal("ru 11 should use many")
	}
	if Plural(LocaleEN, "one", "few", "many", 2) != "many" {
		t.Fatal("en 2 should use many/other")
	}
}
