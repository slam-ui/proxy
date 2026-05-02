package ipv6mitigation

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network_state.json")
	want := State{
		Active:     true,
		Interface:  "Wi-Fi",
		DisabledAt: time.Now().UTC(),
	}
	if err := SaveState(path, want); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	got, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !got.Active || got.Interface != "Wi-Fi" {
		t.Fatalf("state = %+v", got)
	}
}
