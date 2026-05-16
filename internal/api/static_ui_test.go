package api

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

func readStaticText(t *testing.T, path string) string {
	t.Helper()
	data, err := staticFiles.ReadFile(path)
	if err != nil {
		t.Fatalf("read embedded %s: %v", path, err)
	}
	return string(data)
}

var cssAssetPaths = []string{
	"css/00-foundation.css",
	"css/10-home-base.css",
	"css/20-server-panel.css",
	"css/30-pages.css",
	"css/40-overlays.css",
	"css/45-onboarding.css",
	"css/50-responsive.css",
	"css/60-vtb-home.css",
	"css/70-themes.css",
	"css/80-ui-polish.css",
}

var jsAssetPaths = []string{
	"js/00-core.js",
	"js/05-i18n.js",
	"js/10-servers.js",
	"js/20-navigation.js",
	"js/30-rules.js",
	"js/35-processes.js",
	"js/40-logs.js",
	"js/50-settings-theme.js",
	"js/55-onboarding.js",
	"js/60-setup.js",
	"js/70-runtime-polling.js",
	"js/80-chart-utils-init.js",
}

func TestOnboardingUIAssets(t *testing.T) {
	html := readStaticText(t, "static/index.html")
	js := readStaticBundle(t, jsAssetPaths...)
	for _, required := range []string{
		`id="onboardingOv"`,
		`id="onboardingBody"`,
		`async function initOnboarding()`,
		`/onboarding/status`,
		`/onboarding/complete`,
		`/onboarding/skip`,
		`onboardingRunTest`,
		`/servers/auto-connect`,
		`/diagnostics/test`,
		`restartOnboarding()`,
		`onboardingApplyTelemetry`,
		`onboarding.telemetry.title`,
		`telemetry: {enabled, crash_reports: enabled, usage_events: enabled}`,
	} {
		if !strings.Contains(html+js, required) {
			t.Fatalf("onboarding UI missing %q", required)
		}
	}
}

func TestServerEmptyStateGuidesImport(t *testing.T) {
	js := readStaticBundle(t, jsAssetPaths...)
	for _, required := range []string{
		`У вас пока нет серверов`,
		`openImportSettings('subscription')`,
		`openImportSettings('key')`,
		`Где найти серверы`,
	} {
		if !strings.Contains(js, required) {
			t.Fatalf("server empty state missing %q", required)
		}
	}
}

func TestLocalHelpPageIsEmbedded(t *testing.T) {
	help := readStaticText(t, "static/help.html")
	js := readStaticBundle(t, jsAssetPaths...)
	html := readStaticText(t, "static/index.html")
	for _, required := range []string{
		`Quick start`,
		`Troubleshooting`,
		`FAQ`,
		`openLocalHelp()`,
		`/help.html`,
	} {
		if !strings.Contains(help+js+html, required) {
			t.Fatalf("local help missing %q", required)
		}
	}
}

func TestAdvancedSettingsAreProgressivelyDisclosed(t *testing.T) {
	html := readStaticText(t, "static/index.html")
	css := readStaticBundle(t, cssAssetPaths...)
	js := readStaticBundle(t, jsAssetPaths...)
	for _, required := range []string{
		`id="advancedSettingsToggle"`,
		`advanced-setting`,
		`show-advanced-settings`,
		`function toggleAdvancedSettings()`,
	} {
		if !strings.Contains(html+css+js, required) {
			t.Fatalf("advanced disclosure missing %q", required)
		}
	}
}

func TestTelemetryPrivacyControlsExist(t *testing.T) {
	html := readStaticText(t, "static/index.html")
	js := readStaticBundle(t, jsAssetPaths...)
	for _, required := range []string{
		`id="telemetryEnabledToggle"`,
		`id="telemetryCrashToggle"`,
		`id="telemetryUsageToggle"`,
		`function currentTelemetrySettings()`,
		`telemetry: currentTelemetrySettings()`,
		`function toggleTelemetryOption(key)`,
		`/telemetry/delete`,
		`/telemetry/export`,
	} {
		if !strings.Contains(html+js, required) {
			t.Fatalf("telemetry privacy controls missing %q", required)
		}
	}
}

func TestStaticI18nKeysExistInLocales(t *testing.T) {
	html := readStaticText(t, "static/index.html")
	js := readStaticBundle(t, jsAssetPaths...)
	re := regexp.MustCompile(`data-i18n(?:-[a-z-]+)?="([^"]+)"`)
	keys := map[string]bool{}
	for _, match := range re.FindAllStringSubmatch(html+js, -1) {
		keys[match[1]] = true
	}
	trRe := regexp.MustCompile(`\btr\('([^']+)'`)
	for _, match := range trRe.FindAllStringSubmatch(js, -1) {
		keys[match[1]] = true
	}
	if len(keys) == 0 {
		t.Fatal("static UI has no i18n keys")
	}

	for _, path := range []string{"../i18n/locales/ru.json", "../i18n/locales/en.json"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var messages map[string]string
		if err := json.Unmarshal(data, &messages); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for key := range keys {
			if _, ok := messages[key]; !ok {
				t.Fatalf("%s missing UI i18n key %q", path, key)
			}
		}
	}
}

func TestStaticDateFormattingUsesI18nLocale(t *testing.T) {
	js := readStaticBundle(t, jsAssetPaths...)
	for _, forbidden := range []string{`toLocaleDateString('ru-RU'`, `toLocaleTimeString('ru-RU'`, `toLocaleString('ru-RU'`} {
		if strings.Contains(js, forbidden) {
			t.Fatalf("static UI must use i18n date helpers, found %s", forbidden)
		}
	}
	for _, required := range []string{`function formatDate(`, `function formatDateTime(`, `function formatRelativeTime(`} {
		if !strings.Contains(js, required) {
			t.Fatalf("i18n date helper missing %q", required)
		}
	}
}

func TestLocalizedStaticModulesDoNotHardcodeRussianStrings(t *testing.T) {
	cyrillic := regexp.MustCompile(`[А-Яа-яЁё]`)
	for _, path := range []string{
		"static/js/05-i18n.js",
		"static/js/55-onboarding.js",
	} {
		text := readStaticText(t, path)
		if loc := cyrillic.FindStringIndex(text); loc != nil {
			t.Fatalf("%s contains hardcoded Cyrillic near byte %d", path, loc[0])
		}
	}
}

func readStaticBundle(t *testing.T, paths ...string) string {
	t.Helper()
	var b strings.Builder
	for _, path := range paths {
		b.WriteString("\n/* ")
		b.WriteString(path)
		b.WriteString(" */\n")
		b.WriteString(readStaticText(t, "static/"+path))
	}
	return b.String()
}

func TestStaticIndexUsesSplitAssets(t *testing.T) {
	html := readStaticText(t, "static/index.html")
	css := readStaticText(t, "static/styles.css")
	js := readStaticText(t, "static/app.js")

	for _, required := range []string{
		`<link rel="stylesheet" href="styles.css">`,
		`<script src="app.js"></script>`,
		`</body>`,
		`</html>`,
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("index.html missing %q", required)
		}
	}
	if strings.Contains(html, "<style>") || strings.Contains(html, "<script>") {
		t.Fatal("index.html must keep CSS/JS in split static assets")
	}

	for _, path := range cssAssetPaths {
		if !strings.Contains(css, `@import url("`+path+`");`) {
			t.Fatalf("styles.css missing import for %s", path)
		}
		readStaticText(t, "static/"+path)
	}
	for _, path := range jsAssetPaths {
		if !strings.Contains(js, `"`+path+`"`) {
			t.Fatalf("app.js missing loader entry for %s", path)
		}
		readStaticText(t, "static/"+path)
	}
	if strings.Contains(js, "document.write") {
		t.Fatal("app.js must not use document.write for script loading")
	}
}

func TestHomeHeroStatusRowIsHidden(t *testing.T) {
	html := readStaticText(t, "static/index.html")
	css := readStaticText(t, "static/css/80-ui-polish.css")

	for _, required := range []string{`class="hero-status-row"`, `id="orbStage"`, `id="slbl"`} {
		if !strings.Contains(html, required) {
			t.Fatalf("home hero status row missing %q", required)
		}
	}

	hiddenRule := regexp.MustCompile(`(?s)\.hero-status-row\s*\{[^}]*display\s*:\s*none\s*!important`)
	if !hiddenRule.MatchString(css) {
		t.Fatal("home hero status row must stay visually hidden")
	}
	for _, required := range []string{`min-height:118px`, `padding-top:18px`} {
		if !strings.Contains(css, required) {
			t.Fatalf("home hero compact spacing missing %q", required)
		}
	}
}

func TestHomePrimaryAndServerActionsAreSwapped(t *testing.T) {
	html := readStaticText(t, "static/index.html")
	css := readStaticText(t, "static/css/80-ui-polish.css")

	toggleIdx := strings.Index(html, `class="quick-action quick-primary hero-toggle-action"`)
	if toggleIdx < 0 {
		t.Fatal("home toggle action must be rendered in the hero")
	}
	serverIdx := strings.Index(html, `class="srv-pill quick-action server-pill-action wide"`)
	if serverIdx < 0 {
		t.Fatal("server pill must be rendered as a quick action")
	}
	if serverIdx < toggleIdx {
		t.Fatal("server pill must appear below the hero toggle action")
	}
	if strings.Contains(html, `class="quick-action quick-primary wide"`) {
		t.Fatal("old quick-action primary placement must not remain")
	}

	for _, required := range []string{
		`.hero-toggle-action`,
		`.server-pill-action`,
		`grid-column:1/-1`,
		`.server-pill-action .srv-flag`,
		`.server-pill-action .srv-meta`,
	} {
		if !strings.Contains(css, required) {
			t.Fatalf("swapped home action styling missing %q", required)
		}
	}
}

func TestHomeToggleContainsStartupTimer(t *testing.T) {
	html := readStaticText(t, "static/index.html")
	css := readStaticText(t, "static/css/80-ui-polish.css")
	js := readStaticBundle(t, "js/00-core.js", "js/70-runtime-polling.js")

	for _, required := range []string{
		`id="qaTimer"`,
		`id="qaTimerLabel"`,
		`id="qaTimerTime"`,
		`id="qaTimerBar"`,
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("home startup timer markup missing %q", required)
		}
	}
	for _, required := range []string{
		`.hero-toggle-action.timer-active`,
		`.qa-timer.vis`,
		`.qa-timer.indeterminate #qaTimerBar`,
	} {
		if !strings.Contains(css, required) {
			t.Fatalf("home startup timer style missing %q", required)
		}
	}
	for _, required := range []string{
		`ready_at_ms`,
		`curTimer === 'toggle'`,
		`OpTimer.start('warming', label, estMs)`,
	} {
		if !strings.Contains(js, required) {
			t.Fatalf("warming timer logic missing %q", required)
		}
	}
	if strings.Contains(js, `readyAt * 1000 - Date.now()) : 30000`) {
		t.Fatal("warming timer must not use the old fixed 30s fallback")
	}
}

func TestStaticCoreFetchesLoopbackWithTimeout(t *testing.T) {
	js := readStaticText(t, "static/js/00-core.js")
	for _, required := range []string{
		`const API = 'http://127.0.0.1:8080/api';`,
		`const FETCH_TIMEOUT_MS = 10000;`,
		`new AbortController()`,
		`ctrl.abort()`,
	} {
		if !strings.Contains(js, required) {
			t.Fatalf("00-core.js missing %q", required)
		}
	}
}

func TestRulesManualEditorModalUsesExistingJsonFunctions(t *testing.T) {
	html := readStaticText(t, "static/index.html")
	js := readStaticBundle(t, jsAssetPaths...)

	requiredHTML := []string{
		`id="rulesJsonModal"`,
		`onclick="openRulesJsonEditor()"`,
		`id="rulesJson"`,
		`onclick="loadRulesJson()"`,
		`onclick="formatRulesJson()"`,
		`onclick="saveRulesJson()"`,
	}
	for _, s := range requiredHTML {
		if !strings.Contains(html, s) {
			t.Fatalf("index.html missing %q", s)
		}
	}

	requiredJS := []string{
		`function openRulesJsonEditor()`,
		`function closeRulesJsonEditor()`,
		`async function loadRulesJson()`,
		`async function saveRulesJson()`,
	}
	for _, s := range requiredJS {
		if !strings.Contains(js, s) {
			t.Fatalf("UI JS bundle missing %q", s)
		}
	}

	selectRuleAction := regexp.MustCompile(`(?is)<select[^>]+id="(?:ruleMode|modalRuleMode)"`)
	if selectRuleAction.MatchString(html) {
		t.Fatal("rule action controls must not use native select elements")
	}

	for _, id := range []string{"ruleMode", "modalRuleMode"} {
		if !strings.Contains(html, `data-target="`+id+`"`) {
			t.Fatalf("segmented action control missing for %s", id)
		}
	}
}

func TestBackupUIUsesCanonicalRestoreEndpoint(t *testing.T) {
	js := readStaticBundle(t, jsAssetPaths...)

	for _, required := range []string{
		`window.location.href = API + '/backup';`,
		`fd.append('overwrite', 'true');`,
		`fetch(API + '/backup/restore'`,
	} {
		if !strings.Contains(js, required) {
			t.Fatalf("UI JS bundle missing %q", required)
		}
	}
	if strings.Contains(js, `API + '/backup/import'`) || strings.Contains(js, `API + '/backup/export'`) {
		t.Fatal("backup UI must not use deprecated backup/import or backup/export endpoints")
	}
}
