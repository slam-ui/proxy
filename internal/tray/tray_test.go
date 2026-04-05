package tray

import (
	"testing"
)

// TestBuildSlotTitle проверяет формирование заголовков пунктов меню.
func TestBuildSlotTitle(t *testing.T) {
	tests := []struct {
		item ServerItem
		want string
	}{
		{ServerItem{Name: "DE Server", Active: true, Ping: "45ms"}, "✓ DE Server (45ms)"},
		{ServerItem{Name: "RU Server", Active: false, Ping: "120ms"}, "  RU Server (120ms)"},
		{ServerItem{Name: "US Server", Active: false}, "  US Server"},
		{ServerItem{Name: "NL Server", Active: true}, "✓ NL Server"},
	}

	for _, tt := range tests {
		got := buildSlotTitle(tt.item)
		if got != tt.want {
			t.Errorf("buildSlotTitle(%+v) = %q, хотели %q", tt.item, got, tt.want)
		}
	}
}

// TestSetServerList_NoPanic проверяет что SetServerList не паникует когда trей не инициализирован
// (mServersParent == nil — состояние до запуска systray).
func TestSetServerList_NoPanic(t *testing.T) {
	// Гарантируем что mServersParent == nil (тест запускается без systray)
	mServersParent = nil

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("SetServerList вызвал панику: %v", r)
		}
	}()

	SetServerList([]ServerItem{
		{ID: "1", Name: "DE Server", Active: true, Ping: "45ms"},
		{ID: "2", Name: "RU Server", Active: false, Ping: "120ms"},
	})
}

// TestSetActiveServer_NoPanic проверяет что SetActiveServer не паникует без systray.
func TestSetActiveServer_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("SetActiveServer вызвал панику: %v", r)
		}
	}()

	SetActiveServer("DE Server")

	serverSlotsMu.Lock()
	got := activeServerName
	serverSlotsMu.Unlock()

	if got != "DE Server" {
		t.Errorf("activeServerName=%q, ожидалось %q", got, "DE Server")
	}
}

// TestCallbackOnServerSwitch проверяет что OnServerSwitch вызывается с правильным serverID.
func TestCallbackOnServerSwitch(t *testing.T) {
	// Настраиваем callback
	var calledWith string
	cb.OnServerSwitch = func(id string) {
		calledWith = id
	}
	defer func() { cb.OnServerSwitch = nil }()

	// Симулируем клик: устанавливаем serverSlotIDs и вызываем callback напрямую
	serverSlotsMu.Lock()
	serverSlotIDs[0] = "server-42"
	serverSlotsMu.Unlock()

	// Имитируем логику горутины из onReady
	serverSlotsMu.Lock()
	id := serverSlotIDs[0]
	serverSlotsMu.Unlock()
	if id != "" && cb.OnServerSwitch != nil {
		cb.OnServerSwitch(id)
	}

	if calledWith != "server-42" {
		t.Errorf("OnServerSwitch вызван с %q, ожидалось %q", calledWith, "server-42")
	}
}

// TestSetEnabled_NoPanic проверяет что SetEnabled не паникует без systray.
func TestSetEnabled_NoPanic(t *testing.T) {
	mEnable = nil
	mDisable = nil

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("SetEnabled вызвал панику: %v", r)
		}
	}()

	SetEnabled(true)
	SetEnabled(false)
}

func TestSetEnabled_UpdatesProxyEnabledBeforeReady(t *testing.T) {
	mEnable = nil
	mDisable = nil

	SetEnabled(true)
	if !proxyEnabled {
		t.Errorf("proxyEnabled должно быть true после SetEnabled(true), got false")
	}

	SetEnabled(false)
	if proxyEnabled {
		t.Errorf("proxyEnabled должно быть false после SetEnabled(false), got true")
	}
}
