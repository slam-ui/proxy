package tests

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"proxyclient/internal/api"
	"proxyclient/internal/config"
	"proxyclient/internal/logger"
	"proxyclient/internal/proxy"
)

// Mock XRay manager for tests
type mockXRayManager struct{}

func (m *mockXRayManager) Stop() error     { return nil }
func (m *mockXRayManager) IsRunning() bool { return false }
func (m *mockXRayManager) GetPID() int     { return 0 }
func (m *mockXRayManager) Wait() error     { return nil }

// TestIntegrationAPIServer tests the full API server workflow
func TestIntegrationAPIServer(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Setup
	log := &logger.NoOpLogger{}
	proxyMgr := proxy.NewManager(log)
	xrayMgr := &mockXRayManager{}
	server := api.NewServer(api.Config{
		ListenAddress: ":18080", // Use different port for tests
		XRayManager:   xrayMgr,
		ProxyManager:  proxyMgr,
		ConfigPath:    "test-config.json",
		Logger:        log,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start server
	errChan := make(chan error, 1)
	go func() {
		errChan <- server.Start(ctx)
	}()

	// Wait for server to start
	time.Sleep(200 * time.Millisecond)

	// Test 1: Health endpoint
	t.Run("health endpoint", func(t *testing.T) {
		resp, err := http.Get("http://localhost:18080/api/health")
		if err != nil {
			t.Fatalf("Failed to call health endpoint: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected status 200, got %d", resp.StatusCode)
		}

		var result map[string]string
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}

		if result["status"] != "ok" {
			t.Errorf("Expected status 'ok', got '%s'", result["status"])
		}
	})

	// Test 2: Status endpoint
	t.Run("status endpoint", func(t *testing.T) {
		resp, err := http.Get("http://localhost:18080/api/status")
		if err != nil {
			t.Fatalf("Failed to call status endpoint: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected status 200, got %d", resp.StatusCode)
		}

		var result api.StatusResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}

		if result.ConfigPath != "test-config.json" {
			t.Errorf("Expected config path 'test-config.json', got '%s'", result.ConfigPath)
		}
	})

	// Cleanup
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Errorf("Failed to shutdown server: %v", err)
	}

	// Wait for shutdown
	select {
	case err := <-errChan:
		if err != nil {
			t.Logf("Server stopped with: %v", err)
		}
	case <-time.After(12 * time.Second):
		t.Error("Server didn't shut down in time")
	}
}

// TestIntegrationConfigGeneration tests the full config generation workflow
func TestIntegrationConfigGeneration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "integration-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create template file
	templatePath := filepath.Join(tmpDir, "template.json")
	templateContent := `{
		"outbounds": [{
			"settings": {
				"vnext": [{
					"address": "YOUR_SERVER_ADDRESS",
					"port": 0,
					"users": [{"id": "YOUR_UUID", "encryption": "none"}]
				}]
			},
			"streamSettings": {
				"network": "tcp",
				"security": "reality",
				"realitySettings": {
					"fingerprint": "chrome",
					"serverName": "YOUR_SNI",
					"publicKey": "YOUR_PUBLIC_KEY",
					"shortId": "YOUR_SHORT_ID",
					"spiderX": "/"
				}
			}
		}]
	}`
	if err := os.WriteFile(templatePath, []byte(templateContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create secret file
	secretPath := filepath.Join(tmpDir, "secret.key")
	secretContent := "vless://integration-test-uuid@integration.example.com:443?sni=www.google.com&pbk=integration-test-key&sid=int-test"
	if err := os.WriteFile(secretPath, []byte(secretContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Generate runtime config
	outputPath := filepath.Join(tmpDir, "runtime.json")
	err = config.GenerateRuntimeConfig(templatePath, secretPath, outputPath)
	if err != nil {
		t.Fatalf("GenerateRuntimeConfig() failed: %v", err)
	}

	// Verify output file exists
	if _, err := os.Stat(outputPath); os.IsNotExist(err) {
		t.Fatal("Output file was not created")
	}

	// Verify content is valid JSON
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("Output is not valid JSON: %v", err)
	}

	// Verify values were replaced
	outbounds, ok := result["outbounds"].([]interface{})
	if !ok || len(outbounds) == 0 {
		t.Fatal("Expected outbounds array")
	}

	firstOutbound := outbounds[0].(map[string]interface{})
	settings := firstOutbound["settings"].(map[string]interface{})
	vnext := settings["vnext"].([]interface{})
	firstVnext := vnext[0].(map[string]interface{})

	if firstVnext["address"] == "YOUR_SERVER_ADDRESS" {
		t.Error("Address was not replaced")
	}

	if firstVnext["address"] != "integration.example.com" {
		t.Errorf("Expected address 'integration.example.com', got '%v'", firstVnext["address"])
	}

	users := firstVnext["users"].([]interface{})
	firstUser := users[0].(map[string]interface{})
	if firstUser["id"] != "integration-test-uuid" {
		t.Errorf("Expected UUID 'integration-test-uuid', got '%v'", firstUser["id"])
	}

	streamSettings := firstOutbound["streamSettings"].(map[string]interface{})
	realitySettings := streamSettings["realitySettings"].(map[string]interface{})

	if realitySettings["serverName"] != "www.google.com" {
		t.Errorf("Expected SNI 'www.google.com', got '%v'", realitySettings["serverName"])
	}

	if realitySettings["publicKey"] != "integration-test-key" {
		t.Errorf("Expected publicKey 'integration-test-key', got '%v'", realitySettings["publicKey"])
	}

	if realitySettings["shortId"] != "int-test" {
		t.Errorf("Expected shortId 'int-test', got '%v'", realitySettings["shortId"])
	}
}

// TestIntegrationProxyManager tests the proxy manager lifecycle
func TestIntegrationProxyManager(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Note: This test will actually modify Windows registry on Windows
	// Use with caution
	if os.Getenv("RUN_PROXY_TESTS") != "true" {
		t.Skip("Skipping proxy integration test. Set RUN_PROXY_TESTS=true to run")
	}

	log := &logger.NoOpLogger{}
	mgr := proxy.NewManager(log)

	config := proxy.Config{
		Address:  "127.0.0.1:10807",
		Override: "<local>",
	}

	// Test enable
	err := mgr.Enable(config)
	if err != nil {
		t.Fatalf("Enable() failed: %v", err)
	}

	if !mgr.IsEnabled() {
		t.Error("Expected proxy to be enabled")
	}

	// Give system time to apply changes
	time.Sleep(500 * time.Millisecond)

	// Test disable
	err = mgr.Disable()
	if err != nil {
		t.Fatalf("Disable() failed: %v", err)
	}

	if mgr.IsEnabled() {
		t.Error("Expected proxy to be disabled")
	}
}

// BenchmarkIntegrationAPIEndpoints benchmarks API endpoints
func BenchmarkIntegrationAPIEndpoints(b *testing.B) {
	if testing.Short() {
		b.Skip("Skipping integration benchmark in short mode")
	}

	log := &logger.NoOpLogger{}
	proxyMgr := proxy.NewManager(log)

	server := api.NewServer(api.Config{
		ListenAddress: ":18081",
		ProxyManager:  proxyMgr,
		ConfigPath:    "bench-config.json",
		Logger:        log,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go server.Start(ctx)
	time.Sleep(200 * time.Millisecond)

	b.Run("health", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			resp, err := http.Get("http://localhost:18081/api/health")
			if err != nil {
				b.Fatal(err)
			}
			resp.Body.Close()
		}
	})

	b.Run("status", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			resp, err := http.Get("http://localhost:18081/api/status")
			if err != nil {
				b.Fatal(err)
			}
			resp.Body.Close()
		}
	})

	// Cleanup
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	server.Shutdown(shutdownCtx)
}
