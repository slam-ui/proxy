//go:build windows

package main

import "testing"

func TestDefaultGCPercentUsesConservativeDefault(t *testing.T) {
	percent, ok := defaultGCPercent(func(string) string { return "" })
	if !ok {
		t.Fatal("defaultGCPercent disabled tuning without GOGC")
	}
	if percent != defaultGOGC {
		t.Fatalf("defaultGCPercent = %d, want %d", percent, defaultGOGC)
	}
}

func TestDefaultGCPercentHonorsEnvironment(t *testing.T) {
	_, ok := defaultGCPercent(func(key string) string {
		if key == "GOGC" {
			return "100"
		}
		return ""
	})
	if ok {
		t.Fatal("defaultGCPercent enabled tuning despite explicit GOGC")
	}
}
