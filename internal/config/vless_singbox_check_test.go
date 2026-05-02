package config

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const (
	testVLESSUUID     = "12345678-1234-1234-1234-123456789abc"
	testRealityPubKey = "VtAtD2gMlrowmcG--jHT2fJ_E1K3bTF8YrFKuCW53QE"
	testShortID       = "0123456789abcdef"
)

func TestSingBoxCheck_VLESSTransports(t *testing.T) {
	bin := findSingBoxBinaryForTest(t)
	cases := []struct {
		name     string
		vlessURL string
	}{
		{
			name:     "tcp_reality",
			vlessURL: "vless://" + testVLESSUUID + "@example.com:443?encryption=none&security=reality&sni=www.microsoft.com&pbk=" + testRealityPubKey + "&sid=" + testShortID + "&fp=chrome",
		},
		{
			name:     "tcp_tls",
			vlessURL: "vless://" + testVLESSUUID + "@example.com:443?encryption=none&security=tls&sni=example.com",
		},
		{
			name:     "tcp_http_obf",
			vlessURL: "vless://" + testVLESSUUID + "@example.com:443?security=tls&sni=example.com&type=tcp&headerType=http&path=/&host=front.example.com",
		},
		{
			name:     "ws_tls",
			vlessURL: "vless://" + testVLESSUUID + "@example.com:443?security=tls&sni=example.com&type=ws&path=%2Fedge%3Fed%3D2048&host=front.example.com",
		},
		{
			name:     "grpc_reality",
			vlessURL: "vless://" + testVLESSUUID + "@example.com:443?security=reality&type=grpc&serviceName=GunService&mode=gun&sni=www.microsoft.com&pbk=" + testRealityPubKey + "&sid=" + testShortID + "&fp=chrome",
		},
		{
			name:     "http_h2",
			vlessURL: "vless://" + testVLESSUUID + "@example.com:443?security=tls&sni=example.com&type=h2&path=/h2&host=a.example.com,b.example.com&alpn=h2",
		},
		{
			name:     "httpupgrade",
			vlessURL: "vless://" + testVLESSUUID + "@example.com:443?security=tls&sni=example.com&type=httpupgrade&path=/up&host=front.example.com",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params, err := ParseVLESSContent(tc.vlessURL)
			if err != nil {
				t.Fatalf("ParseVLESSContent: %v", err)
			}
			if err := validateVLESSParams(params); err != nil {
				t.Fatalf("validateVLESSParams: %v", err)
			}
			out := buildVLESSOutbound(params)
			cfg := buildSingBoxConfig(out, ipOrEmpty(params.Address), &RoutingConfig{DefaultAction: ActionProxy})
			checkSingBoxConfig(t, bin, cfg)
		})
	}
}

func findSingBoxBinaryForTest(t *testing.T) string {
	t.Helper()
	if env := os.Getenv("SING_BOX_BIN"); env != "" {
		if _, err := os.Stat(env); err != nil {
			t.Fatalf("SING_BOX_BIN=%q недоступен: %v", env, err)
		}
		return env
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	roots := []string{cwd, filepath.Join(cwd, ".."), filepath.Join(cwd, "..", "..")}
	names := []string{"sing-box.exe", "sing-box"}
	for _, root := range roots {
		for _, name := range names {
			candidate := filepath.Clean(filepath.Join(root, name))
			if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
				return candidate
			}
		}
	}
	if path, err := exec.LookPath("sing-box"); err == nil {
		return path
	}
	t.Skip("sing-box binary not found; set SING_BOX_BIN to enable schema check")
	return ""
}

func checkSingBoxConfig(t *testing.T, bin string, cfg *SingBoxConfig) {
	t.Helper()
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent: %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, raw, 0644); err != nil {
		t.Fatalf("WriteFile config: %v", err)
	}
	cmd := exec.Command(bin, "check", "-c", path)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sing-box check failed: %v\n%s\nconfig:\n%s", err, output, raw)
	}
}
