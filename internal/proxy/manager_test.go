package proxy

import (
	"sync"
	"testing"

	"proxyclient/internal/logger"
)

func TestNewManager(t *testing.T) {
	log := &logger.NoOpLogger{}
	mgr := NewManager(log)

	if mgr == nil {
		t.Fatal("Expected manager, got nil")
	}

	if mgr.IsEnabled() {
		t.Error("Expected proxy to be disabled initially")
	}
}

func TestManager_Enable(t *testing.T) {
	t.Run("valid config", func(t *testing.T) {
		mgr := NewManager(&logger.NoOpLogger{})

		config := Config{
			Address:  "127.0.0.1:8080",
			Override: "<local>",
		}

		// Note: This will actually try to set system proxy on Windows
		// In a real test environment, you might want to mock this
		err := mgr.Enable(config)

		// Clean up
		defer mgr.Disable()

		if err != nil {
			// On non-Windows or without permissions, this might fail
			// That's okay for unit tests
			t.Logf("Enable() failed (expected on some systems): %v", err)
			return
		}

		if !mgr.IsEnabled() {
			t.Error("Expected proxy to be enabled")
		}

		gotConfig := mgr.GetConfig()
		if gotConfig.Address != config.Address {
			t.Errorf("Config address = %v, want %v", gotConfig.Address, config.Address)
		}

		if gotConfig.Override != config.Override {
			t.Errorf("Config override = %v, want %v", gotConfig.Override, config.Override)
		}
	})

	t.Run("invalid config - empty address", func(t *testing.T) {
		mgr := NewManager(&logger.NoOpLogger{})

		config := Config{
			Address:  "",
			Override: "<local>",
		}

		err := mgr.Enable(config)
		if err == nil {
			t.Error("Expected error for empty address")
		}
	})

	t.Run("enable twice", func(t *testing.T) {
		mgr := NewManager(&logger.NoOpLogger{})
		defer mgr.Disable()

		config := Config{
			Address:  "127.0.0.1:8080",
			Override: "<local>",
		}

		// First enable
		_ = mgr.Enable(config)

		// Second enable - should still work
		err := mgr.Enable(config)
		if err != nil {
			t.Logf("Second Enable() failed: %v", err)
		}
	})
}

func TestManager_Disable(t *testing.T) {
	t.Run("disable enabled proxy", func(t *testing.T) {
		mgr := NewManager(&logger.NoOpLogger{})

		config := Config{
			Address:  "127.0.0.1:8080",
			Override: "<local>",
		}

		// Enable first
		_ = mgr.Enable(config)

		// Then disable
		err := mgr.Disable()
		if err != nil {
			t.Logf("Disable() failed: %v", err)
		}

		if mgr.IsEnabled() {
			t.Error("Expected proxy to be disabled")
		}
	})

	t.Run("disable already disabled", func(t *testing.T) {
		mgr := NewManager(&logger.NoOpLogger{})

		// Disable without enabling first
		err := mgr.Disable()
		if err != nil {
			t.Errorf("Disable() on already disabled proxy should not error, got: %v", err)
		}
	})
}

func TestManager_IsEnabled(t *testing.T) {
	mgr := NewManager(&logger.NoOpLogger{})

	// Initially disabled
	if mgr.IsEnabled() {
		t.Error("Expected proxy to be disabled initially")
	}

	// After enable
	config := Config{
		Address:  "127.0.0.1:8080",
		Override: "<local>",
	}
	_ = mgr.Enable(config)

	// Clean up at end
	defer mgr.Disable()

	if !mgr.IsEnabled() {
		t.Error("Expected proxy to be enabled after Enable()")
	}

	// After disable
	mgr.Disable()

	if mgr.IsEnabled() {
		t.Error("Expected proxy to be disabled after Disable()")
	}
}

func TestManager_GetConfig(t *testing.T) {
	mgr := NewManager(&logger.NoOpLogger{})

	// Initially empty config
	config := mgr.GetConfig()
	if config.Address != "" {
		t.Errorf("Expected empty address initially, got %v", config.Address)
	}

	// After setting config
	newConfig := Config{
		Address:  "127.0.0.1:8080",
		Override: "<local>",
	}
	_ = mgr.Enable(newConfig)
	defer mgr.Disable()

	gotConfig := mgr.GetConfig()
	if gotConfig.Address != newConfig.Address {
		t.Errorf("GetConfig().Address = %v, want %v", gotConfig.Address, newConfig.Address)
	}

	if gotConfig.Override != newConfig.Override {
		t.Errorf("GetConfig().Override = %v, want %v", gotConfig.Override, newConfig.Override)
	}
}

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr bool
	}{
		{
			name: "valid config",
			config: Config{
				Address:  "127.0.0.1:8080",
				Override: "<local>",
			},
			wantErr: false,
		},
		{
			name: "valid config with domain",
			config: Config{
				Address:  "proxy.example.com:3128",
				Override: "*.local",
			},
			wantErr: false,
		},
		{
			name: "empty address",
			config: Config{
				Address:  "",
				Override: "<local>",
			},
			wantErr: true,
		},
		{
			name: "only whitespace address",
			config: Config{
				Address:  "   ",
				Override: "<local>",
			},
			wantErr: true,
		},
		{
			name: "empty override is valid",
			config: Config{
				Address:  "127.0.0.1:8080",
				Override: "",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateConfig(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestManager_ThreadSafety(t *testing.T) {
	mgr := NewManager(&logger.NoOpLogger{})
	defer mgr.Disable()

	config := Config{
		Address:  "127.0.0.1:8080",
		Override: "<local>",
	}

	var wg sync.WaitGroup
	errChan := make(chan error, 100)

	// Concurrent Enable/Disable
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := mgr.Enable(config); err != nil {
				errChan <- err
			}
		}()

		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := mgr.Disable(); err != nil {
				errChan <- err
			}
		}()
	}

	// Concurrent reads
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mgr.IsEnabled()
			mgr.GetConfig()
		}()
	}

	wg.Wait()
	close(errChan)

	// Check for any errors
	for err := range errChan {
		t.Logf("Concurrent operation error: %v", err)
	}
}

func TestManager_StateTransitions(t *testing.T) {
	mgr := NewManager(&logger.NoOpLogger{})

	config := Config{
		Address:  "127.0.0.1:8080",
		Override: "<local>",
	}

	// Disabled -> Enabled
	if mgr.IsEnabled() {
		t.Error("Initial state should be disabled")
	}

	_ = mgr.Enable(config)
	if !mgr.IsEnabled() {
		t.Error("Should be enabled after Enable()")
	}

	// Enabled -> Disabled
	_ = mgr.Disable()
	if mgr.IsEnabled() {
		t.Error("Should be disabled after Disable()")
	}

	// Disabled -> Disabled (idempotent)
	_ = mgr.Disable()
	if mgr.IsEnabled() {
		t.Error("Should remain disabled")
	}

	// Disabled -> Enabled -> Enabled (idempotent)
	_ = mgr.Enable(config)
	_ = mgr.Enable(config)
	if !mgr.IsEnabled() {
		t.Error("Should be enabled")
	}

	// Clean up
	mgr.Disable()
}

func BenchmarkManager_Enable(b *testing.B) {
	mgr := NewManager(&logger.NoOpLogger{})
	defer mgr.Disable()

	config := Config{
		Address:  "127.0.0.1:8080",
		Override: "<local>",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = mgr.Enable(config)
	}
}

func BenchmarkManager_IsEnabled(b *testing.B) {
	mgr := NewManager(&logger.NoOpLogger{})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mgr.IsEnabled()
	}
}

func BenchmarkManager_GetConfig(b *testing.B) {
	mgr := NewManager(&logger.NoOpLogger{})
	config := Config{
		Address:  "127.0.0.1:8080",
		Override: "<local>",
	}
	_ = mgr.Enable(config)
	defer mgr.Disable()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mgr.GetConfig()
	}
}
