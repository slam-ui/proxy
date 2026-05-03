package window

import "testing"

func TestCloseToTrayDefaultsOn(t *testing.T) {
	SetCloseToTray(true)
	if !CloseToTray() {
		t.Fatal("CloseToTray() = false, want true")
	}
}

func TestSetCloseToTray(t *testing.T) {
	SetCloseToTray(false)
	if CloseToTray() {
		t.Fatal("CloseToTray() = true, want false")
	}
	SetCloseToTray(true)
}
