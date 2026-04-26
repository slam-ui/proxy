package tray

import (
	"testing"
)

// TestSetServerList_NoPanic проверяет что SetServerList не паникует когда трей не инициализирован.
func TestSetServerList_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("SetServerList вызвал панику: %v", r)
		}
	}()

	SetServerList([]ServerItem{
		{ID: "1", Name: "DE Server", Active: true, Ping: "45ms"},
		{ID: "2", Name: "RU Server", Active: false, Ping: "120ms"},
	})

	win32MenuState.Lock()
	n := len(win32MenuState.servers)
	win32MenuState.Unlock()

	if n != 2 {
		t.Errorf("ожидалось 2 сервера в win32MenuState, получено %d", n)
	}
}

// TestSetServerList_TruncatesAtMax проверяет что сверх maxServerSlots серверы обрезаются.
func TestSetServerList_TruncatesAtMax(t *testing.T) {
	items := make([]ServerItem, maxServerSlots+5)
	for i := range items {
		items[i] = ServerItem{ID: "x", Name: "S"}
	}
	SetServerList(items)

	win32MenuState.Lock()
	n := len(win32MenuState.servers)
	win32MenuState.Unlock()

	if n > maxServerSlots {
		t.Errorf("ожидалось не более %d серверов, получено %d", maxServerSlots, n)
	}
}

// TestSetActiveServer_NoPanic проверяет что SetActiveServer не паникует без трея.
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

// TestSetEnabled_NoPanic проверяет что SetEnabled не паникует без инициализированного трея.
// win32hwnd == 0 → win32SetIcon и win32SetTooltip возвращаются сразу.
func TestSetEnabled_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("SetEnabled вызвал панику: %v", r)
		}
	}()

	SetEnabled(true)
	SetEnabled(false)
}

// TestSetEnabled_UpdatesMenuState проверяет что SetEnabled корректно обновляет флаги меню.
func TestSetEnabled_UpdatesMenuState(t *testing.T) {
	SetEnabled(true)
	win32MenuState.Lock()
	en := win32MenuState.enableEnabled
	dis := win32MenuState.disableEnabled
	win32MenuState.Unlock()
	if en {
		t.Error("после SetEnabled(true): enableEnabled должно быть false")
	}
	if !dis {
		t.Error("после SetEnabled(true): disableEnabled должно быть true")
	}

	SetEnabled(false)
	win32MenuState.Lock()
	en = win32MenuState.enableEnabled
	dis = win32MenuState.disableEnabled
	win32MenuState.Unlock()
	if !en {
		t.Error("после SetEnabled(false): enableEnabled должно быть true")
	}
	if dis {
		t.Error("после SetEnabled(false): disableEnabled должно быть false")
	}
}

// TestSetProxyAddr обновляет copyAddr и не паникует.
func TestSetProxyAddr(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("SetProxyAddr вызвал панику: %v", r)
		}
	}()

	SetProxyAddr("127.0.0.1:10808")

	win32MenuState.Lock()
	addr := win32MenuState.copyAddr
	win32MenuState.Unlock()

	if addr != "127.0.0.1:10808" {
		t.Errorf("copyAddr=%q, ожидалось %q", addr, "127.0.0.1:10808")
	}
}

// TestBuildTooltip_Disabled проверяет тултип в выключенном состоянии.
func TestBuildTooltip_Disabled(t *testing.T) {
	got := buildTooltip(false)
	if got != "SafeSky — Выключен" {
		t.Errorf("buildTooltip(false)=%q", got)
	}
}

// TestBuildTooltip_EnabledWithActiveServer проверяет что имя активного сервера добавляется в тултип.
func TestBuildTooltip_EnabledWithActiveServer(t *testing.T) {
	SetServerList([]ServerItem{
		{ID: "1", Name: "DE-1", Active: false},
		{ID: "2", Name: "FR-2", Active: true},
	})

	got := buildTooltip(true)
	want := "SafeSky — Включён  FR-2"
	if got != want {
		t.Errorf("buildTooltip(true)=%q, ожидалось %q", got, want)
	}
}

// TestCallbackOnServerSwitch проверяет что OnServerSwitch вызывается с правильным serverID.
func TestCallbackOnServerSwitch(t *testing.T) {
	var calledWith string
	cb.OnServerSwitch = func(id string) {
		calledWith = id
	}
	defer func() { cb.OnServerSwitch = nil }()

	SetServerList([]ServerItem{
		{ID: "server-42", Name: "DE Server", Active: false},
	})

	// Вызываем напрямую логику из handleMenuCommand
	handleMenuCommand(idSrvBase + 0)

	if calledWith != "server-42" {
		t.Errorf("OnServerSwitch вызван с %q, ожидалось %q", calledWith, "server-42")
	}
}
