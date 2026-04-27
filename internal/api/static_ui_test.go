package api

import (
	"regexp"
	"strings"
	"testing"
)

func TestRulesManualEditorModalUsesExistingJsonFunctions(t *testing.T) {
	data, err := staticFiles.ReadFile("static/index.html")
	if err != nil {
		t.Fatalf("read embedded index.html: %v", err)
	}
	html := string(data)

	required := []string{
		`id="rulesJsonModal"`,
		`onclick="openRulesJsonEditor()"`,
		`id="rulesJson"`,
		`onclick="loadRulesJson()"`,
		`onclick="formatRulesJson()"`,
		`onclick="saveRulesJson()"`,
		`function openRulesJsonEditor()`,
		`function closeRulesJsonEditor()`,
		`async function loadRulesJson()`,
		`async function saveRulesJson()`,
	}
	for _, s := range required {
		if !strings.Contains(html, s) {
			t.Fatalf("index.html missing %q", s)
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
	data, err := staticFiles.ReadFile("static/index.html")
	if err != nil {
		t.Fatalf("read embedded index.html: %v", err)
	}
	html := string(data)

	for _, required := range []string{
		`window.location.href = API + '/backup';`,
		`fd.append('overwrite', 'true');`,
		`fetch(API + '/backup/restore'`,
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("index.html missing %q", required)
		}
	}
	if strings.Contains(html, `API + '/backup/import'`) || strings.Contains(html, `API + '/backup/export'`) {
		t.Fatal("backup UI must not use deprecated backup/import or backup/export endpoints")
	}
}
