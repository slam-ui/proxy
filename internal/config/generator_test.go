package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseVLESSKey(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr bool
		check   func(*testing.T, *VLESSParams)
	}{
		{
			name:    "valid vless url",
			content: "vless://12345678-1234-1234-1234-123456789abc@example.com:443?sni=www.google.com&pbk=abcdefghijklmnop&sid=abc123",
			wantErr: false,
			check: func(t *testing.T, params *VLESSParams) {
				if params.Address != "example.com" {
					t.Errorf("Address = %v, want example.com", params.Address)
				}
				if params.Port != 443 {
					t.Errorf("Port = %v, want 443", params.Port)
				}
				if params.UUID != "12345678-1234-1234-1234-123456789abc" {
					t.Errorf("UUID = %v, want 12345678-1234-1234-1234-123456789abc", params.UUID)
				}
				if params.SNI != "www.google.com" {
					t.Errorf("SNI = %v, want www.google.com", params.SNI)
				}
				if params.PublicKey != "abcdefghijklmnop" {
					t.Errorf("PublicKey = %v, want abcdefghijklmnop", params.PublicKey)
				}
				if params.ShortID != "abc123" {
					t.Errorf("ShortID = %v, want abc123", params.ShortID)
				}
			},
		},
		{
			name:    "invalid protocol",
			content: "http://example.com:443",
			wantErr: true,
		},
		{
			name:    "missing port",
			content: "vless://uuid@example.com?sni=example.com&pbk=key&sid=id",
			wantErr: true,
		},
		{
			name:    "invalid port",
			content: "vless://uuid@example.com:abc?sni=example.com&pbk=key&sid=id",
			wantErr: true,
		},
		{
			name:    "empty file",
			content: "",
			wantErr: true,
		},
		{
			name:    "whitespace only",
			content: "   \n\t  ",
			wantErr: true,
		},
		{
			name:    "missing query parameters",
			content: "vless://uuid@example.com:443",
			wantErr: false,
			check: func(t *testing.T, params *VLESSParams) {
				// Should parse but will fail validation later
				if params.SNI != "" {
					t.Errorf("Expected empty SNI, got %v", params.SNI)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp file
			tmpfile, err := os.CreateTemp("", "secret-*.key")
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(tmpfile.Name())

			if _, err := tmpfile.Write([]byte(tt.content)); err != nil {
				t.Fatal(err)
			}
			tmpfile.Close()

			params, err := parseVLESSKey(tmpfile.Name())
			if (err != nil) != tt.wantErr {
				t.Errorf("parseVLESSKey() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && tt.check != nil {
				tt.check(t, params)
			}
		})
	}
}

func TestParseVLESSKey_FileErrors(t *testing.T) {
	t.Run("file not found", func(t *testing.T) {
		_, err := parseVLESSKey("nonexistent-file.key")
		if err == nil {
			t.Error("Expected error for nonexistent file")
		}
	})
}

func TestValidateVLESSParams(t *testing.T) {
	tests := []struct {
		name    string
		params  *VLESSParams
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid params",
			params: &VLESSParams{
				Address:   "example.com",
				Port:      443,
				UUID:      "12345678-1234-1234-1234-123456789abc",
				SNI:       "www.google.com",
				PublicKey: "abcdefghijklmnop",
				ShortID:   "abc123",
			},
			wantErr: false,
		},
		{
			name: "missing address",
			params: &VLESSParams{
				Port:      443,
				UUID:      "uuid",
				SNI:       "sni",
				PublicKey: "key",
				ShortID:   "id",
			},
			wantErr: true,
			errMsg:  "отсутствует адрес сервера",
		},
		{
			name: "missing UUID",
			params: &VLESSParams{
				Address:   "example.com",
				Port:      443,
				SNI:       "sni",
				PublicKey: "key",
				ShortID:   "id",
			},
			wantErr: true,
			errMsg:  "отсутствует UUID",
		},
		{
			name: "missing SNI",
			params: &VLESSParams{
				Address:   "example.com",
				Port:      443,
				UUID:      "uuid",
				PublicKey: "key",
				ShortID:   "id",
			},
			wantErr: true,
			errMsg:  "отсутствует SNI",
		},
		{
			name: "missing public key",
			params: &VLESSParams{
				Address: "example.com",
				Port:    443,
				UUID:    "uuid",
				SNI:     "sni",
				ShortID: "id",
			},
			wantErr: true,
			errMsg:  "отсутствует публичный ключ",
		},
		{
			name: "missing short ID",
			params: &VLESSParams{
				Address:   "example.com",
				Port:      443,
				UUID:      "uuid",
				SNI:       "sni",
				PublicKey: "key",
			},
			wantErr: true,
			errMsg:  "отсутствует ShortID",
		},
		{
			name: "port too low",
			params: &VLESSParams{
				Address:   "example.com",
				Port:      0,
				UUID:      "uuid",
				SNI:       "sni",
				PublicKey: "key",
				ShortID:   "id",
			},
			wantErr: true,
			errMsg:  "некорректный порт",
		},
		{
			name: "port too high",
			params: &VLESSParams{
				Address:   "example.com",
				Port:      70000,
				UUID:      "uuid",
				SNI:       "sni",
				PublicKey: "key",
				ShortID:   "id",
			},
			wantErr: true,
			errMsg:  "некорректный порт",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateVLESSParams(tt.params)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateVLESSParams() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoadTemplate(t *testing.T) {
	t.Run("valid template", func(t *testing.T) {
		// Create temp template file
		tmpfile, err := os.CreateTemp("", "template-*.json")
		if err != nil {
			t.Fatal(err)
		}
		defer os.Remove(tmpfile.Name())

		templateContent := `{
			"outbounds": [
				{
					"settings": {
						"vnext": [
							{
								"address": "YOUR_SERVER_ADDRESS",
								"port": 0,
								"users": [{"id": "YOUR_UUID"}]
							}
						]
					},
					"streamSettings": {
						"realitySettings": {
							"serverName": "YOUR_SNI",
							"publicKey": "YOUR_PUBLIC_KEY",
							"shortId": "YOUR_SHORT_ID"
						}
					}
				}
			]
		}`

		if _, err := tmpfile.Write([]byte(templateContent)); err != nil {
			t.Fatal(err)
		}
		tmpfile.Close()

		config, err := loadTemplate(tmpfile.Name())
		if err != nil {
			t.Fatalf("loadTemplate() failed: %v", err)
		}

		if config == nil {
			t.Fatal("Expected config, got nil")
		}

		outbounds, ok := config["outbounds"].([]interface{})
		if !ok {
			t.Fatal("Expected outbounds array")
		}

		if len(outbounds) == 0 {
			t.Fatal("Expected at least one outbound")
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		tmpfile, err := os.CreateTemp("", "template-*.json")
		if err != nil {
			t.Fatal(err)
		}
		defer os.Remove(tmpfile.Name())

		if _, err := tmpfile.Write([]byte("invalid json")); err != nil {
			t.Fatal(err)
		}
		tmpfile.Close()

		_, err = loadTemplate(tmpfile.Name())
		if err == nil {
			t.Error("Expected error for invalid JSON")
		}
	})

	t.Run("file not found", func(t *testing.T) {
		_, err := loadTemplate("nonexistent.json")
		if err == nil {
			t.Error("Expected error for nonexistent file")
		}
	})
}

func TestUpdateConfig(t *testing.T) {
	t.Run("valid update", func(t *testing.T) {
		config := map[string]interface{}{
			"outbounds": []interface{}{
				map[string]interface{}{
					"settings": map[string]interface{}{
						"vnext": []interface{}{
							map[string]interface{}{
								"address": "old_address",
								"port":    float64(0),
								"users": []interface{}{
									map[string]interface{}{
										"id": "old_uuid",
									},
								},
							},
						},
					},
					"streamSettings": map[string]interface{}{
						"realitySettings": map[string]interface{}{
							"serverName": "old_sni",
							"publicKey":  "old_key",
							"shortId":    "old_id",
						},
					},
				},
			},
		}

		params := &VLESSParams{
			Address:   "new.example.com",
			Port:      443,
			UUID:      "new-uuid",
			SNI:       "new.sni.com",
			PublicKey: "new-public-key",
			ShortID:   "new-short-id",
		}

		err := updateConfig(config, params)
		if err != nil {
			t.Fatalf("updateConfig() failed: %v", err)
		}

		// Verify updates
		outbounds := config["outbounds"].([]interface{})
		firstOutbound := outbounds[0].(map[string]interface{})
		settings := firstOutbound["settings"].(map[string]interface{})
		vnext := settings["vnext"].([]interface{})
		firstVnext := vnext[0].(map[string]interface{})

		if firstVnext["address"] != params.Address {
			t.Errorf("Address not updated: got %v, want %v", firstVnext["address"], params.Address)
		}

		if firstVnext["port"] != params.Port {
			t.Errorf("Port not updated: got %v, want %v", firstVnext["port"], params.Port)
		}

		users := firstVnext["users"].([]interface{})
		firstUser := users[0].(map[string]interface{})
		if firstUser["id"] != params.UUID {
			t.Errorf("UUID not updated: got %v, want %v", firstUser["id"], params.UUID)
		}

		streamSettings := firstOutbound["streamSettings"].(map[string]interface{})
		realitySettings := streamSettings["realitySettings"].(map[string]interface{})

		if realitySettings["serverName"] != params.SNI {
			t.Errorf("SNI not updated: got %v, want %v", realitySettings["serverName"], params.SNI)
		}

		if realitySettings["publicKey"] != params.PublicKey {
			t.Errorf("PublicKey not updated: got %v, want %v", realitySettings["publicKey"], params.PublicKey)
		}

		if realitySettings["shortId"] != params.ShortID {
			t.Errorf("ShortID not updated: got %v, want %v", realitySettings["shortId"], params.ShortID)
		}
	})

	t.Run("missing outbounds", func(t *testing.T) {
		config := map[string]interface{}{}
		params := &VLESSParams{Address: "example.com", Port: 443}

		err := updateConfig(config, params)
		if err == nil {
			t.Error("Expected error for missing outbounds")
		}
	})
}

func TestSaveConfig(t *testing.T) {
	t.Run("valid save", func(t *testing.T) {
		tmpfile, err := os.CreateTemp("", "config-*.json")
		if err != nil {
			t.Fatal(err)
		}
		tmpfile.Close()
		defer os.Remove(tmpfile.Name())

		config := map[string]interface{}{
			"test": "value",
		}

		err = saveConfig(config, tmpfile.Name())
		if err != nil {
			t.Fatalf("saveConfig() failed: %v", err)
		}

		// Verify file was written
		data, err := os.ReadFile(tmpfile.Name())
		if err != nil {
			t.Fatal(err)
		}

		if len(data) == 0 {
			t.Error("Expected data to be written")
		}
	})

	t.Run("invalid path", func(t *testing.T) {
		config := map[string]interface{}{"test": "value"}
		err := saveConfig(config, "/nonexistent/path/config.json")
		if err == nil {
			t.Error("Expected error for invalid path")
		}
	})
}

func TestGenerateRuntimeConfig_Integration(t *testing.T) {
	// Create temporary directory
	tmpDir, err := os.MkdirTemp("", "config-test-*")
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
					"users": [{"id": "YOUR_UUID"}]
				}]
			},
			"streamSettings": {
				"realitySettings": {
					"serverName": "YOUR_SNI",
					"publicKey": "YOUR_PUBLIC_KEY",
					"shortId": "YOUR_SHORT_ID"
				}
			}
		}]
	}`
	if err := os.WriteFile(templatePath, []byte(templateContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create secret file
	secretPath := filepath.Join(tmpDir, "secret.key")
	secretContent := "vless://test-uuid@example.com:443?sni=www.google.com&pbk=test-key&sid=test-id"
	if err := os.WriteFile(secretPath, []byte(secretContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Generate runtime config
	outputPath := filepath.Join(tmpDir, "runtime.json")
	err = GenerateRuntimeConfig(templatePath, secretPath, outputPath)
	if err != nil {
		t.Fatalf("GenerateRuntimeConfig() failed: %v", err)
	}

	// Verify output file exists
	if _, err := os.Stat(outputPath); os.IsNotExist(err) {
		t.Fatal("Output file was not created")
	}

	// Verify content
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}

	if len(data) == 0 {
		t.Error("Output file is empty")
	}
}
