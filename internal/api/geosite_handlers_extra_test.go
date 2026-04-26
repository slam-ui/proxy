package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"proxyclient/internal/config"
	"proxyclient/internal/logger"
)

func TestGeositeRuleNamesFromConfig_DedupAndValidate(t *testing.T) {
	names := geositeRuleNamesFromConfig(&config.RoutingConfig{
		Rules: []config.RoutingRule{
			{Value: "geosite:YouTube", Type: config.RuleTypeGeosite},
			{Value: "youtube", Type: config.RuleTypeGeosite},
			{Value: "geosite:youtube", Type: config.RuleTypeDomain},
			{Value: "geosite:../bad", Type: config.RuleTypeGeosite},
			{Value: "example.com", Type: config.RuleTypeDomain},
		},
	})

	if len(names) != 1 || names[0] != "youtube" {
		t.Fatalf("names = %#v, want [youtube]", names)
	}
}

func TestGeositeList_UsesSRSHeaderInsteadOfSize(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer os.Chdir(old)

	if err := os.MkdirAll(config.DataDir, 0755); err != nil {
		t.Fatalf("MkdirAll data: %v", err)
	}
	if err := os.WriteFile(filepath.Join(config.DataDir, "geosite-openai.bin"), validTestSRS(159), 0644); err != nil {
		t.Fatalf("WriteFile openai: %v", err)
	}
	if err := os.WriteFile(filepath.Join(config.DataDir, "geosite-localbad.bin"), []byte("<!doctype html>"), 0644); err != nil {
		t.Fatalf("WriteFile localbad: %v", err)
	}

	srv := NewServer(Config{
		XRayManager:  &stubXray{},
		ProxyManager: &stubProxy{},
		Logger:       &logger.NoOpLogger{},
	}, context.Background())

	req := httptest.NewRequest(http.MethodGet, "/api/geosite", nil)
	w := httptest.NewRecorder()
	srv.handleGeositeList(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200: %s", w.Code, w.Body.String())
	}
	var resp GeositeListResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	items := map[string]GeositeInfo{}
	for _, item := range resp.Items {
		items[item.Name] = item
	}
	if item := items["openai"]; !item.Available || item.FileSize != 159 {
		t.Fatalf("openai item = %+v, want available small SRS", item)
	}
	if item := items["localbad"]; item.Available {
		t.Fatalf("localbad item = %+v, want unavailable HTML", item)
	}
}
