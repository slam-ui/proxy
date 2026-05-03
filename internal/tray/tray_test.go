package tray

import (
	"testing"

	"proxyclient/internal/hotkeys"
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

func TestSetServerList_SortsByPing(t *testing.T) {
	SetServerList([]ServerItem{
		{ID: "slow", Name: "slow", Ping: "120ms"},
		{ID: "fast", Name: "fast", Ping: "23ms"},
		{ID: "unknown", Name: "unknown"},
	})
	win32MenuState.Lock()
	got := append([]ServerItem(nil), win32MenuState.servers...)
	win32MenuState.Unlock()
	if got[0].ID != "fast" || got[1].ID != "slow" || got[2].ID != "unknown" {
		t.Fatalf("server order = %+v", got)
	}
}

func TestSetProfileListAndActive(t *testing.T) {
	SetProfileList([]ProfileItem{{Name: "Default"}, {Name: "Work"}})
	SetActiveProfile("Work")
	win32MenuState.Lock()
	got := append([]ProfileItem(nil), win32MenuState.profiles...)
	win32MenuState.Unlock()
	if len(got) != 2 || got[0].Active || !got[1].Active {
		t.Fatalf("profiles = %+v", got)
	}
}

func TestSetTrafficSpeed(t *testing.T) {
	SetTrafficSpeed(230*1024, 4*1024*1024+200*1024)
	win32MenuState.Lock()
	got := win32MenuState.speedText
	win32MenuState.Unlock()
	if got != "↓ 4.2 MB/s ↑ 230 KB/s" {
		t.Fatalf("speedText=%q", got)
	}
}

func TestTrayConnectedStatusText(t *testing.T) {
	SetLanguage("ru")
	got := trayConnectedStatusText([]ServerItem{{Name: "slow"}, {Name: "fast", Active: true}})
	if got != "Туннель включён — fast" {
		t.Fatalf("status=%q", got)
	}
}

func TestTrayLanguageEnglish(t *testing.T) {
	SetLanguage("en")
	t.Cleanup(func() { SetLanguage("ru") })

	if got := trayConnectedStatusText([]ServerItem{{Name: "fast", Active: true}}); got != "Tunnel connected — fast" {
		t.Fatalf("english status=%q", got)
	}
	if got := buildTooltip(false); got != "SafeSky — tunnel disconnected" {
		t.Fatalf("english tooltip=%q", got)
	}
}

func TestFormatTrafficBytes(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{-1, "0 B"},
		{512, "512 B"},
		{1536, "1.5 KB"},
		{10 * 1024, "10 KB"},
		{4*1024*1024 + 200*1024, "4.2 MB"},
	}
	for _, tt := range tests {
		if got := formatTrafficBytes(tt.bytes); got != tt.want {
			t.Fatalf("formatTrafficBytes(%d)=%q, want %q", tt.bytes, got, tt.want)
		}
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
	SetLanguage("ru")
	got := buildTooltip(false)
	if got != "SafeSky — туннель выключен" {
		t.Errorf("buildTooltip(false)=%q", got)
	}
}

// TestBuildTooltip_EnabledWithActiveServer проверяет что имя активного сервера добавляется в тултип.
func TestBuildTooltip_EnabledWithActiveServer(t *testing.T) {
	SetLanguage("ru")
	SetServerList([]ServerItem{
		{ID: "1", Name: "DE-1", Active: false},
		{ID: "2", Name: "FR-2", Active: true},
	})

	got := buildTooltip(true)
	want := "SafeSky — туннель включён  FR-2"
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

func TestCallbackOnProfileSwitch(t *testing.T) {
	var calledWith int
	cb.OnProfileSwitch = func(index int) { calledWith = index }
	defer func() { cb.OnProfileSwitch = nil }()
	handleMenuCommand(idProfBase + 1)
	if calledWith != 2 {
		t.Fatalf("OnProfileSwitch=%d, want 2", calledWith)
	}
}

func TestHandleTrayToggleCallsEnableDisable(t *testing.T) {
	var enabled, disabled int
	cb.OnEnable = func() { enabled++ }
	cb.OnDisable = func() { disabled++ }
	defer func() {
		cb.OnEnable = nil
		cb.OnDisable = nil
	}()

	SetEnabled(false)
	handleTrayToggle()
	if enabled != 1 || disabled != 0 {
		t.Fatalf("disabled toggle: enabled=%d disabled=%d", enabled, disabled)
	}

	SetEnabled(true)
	handleTrayToggle()
	if enabled != 1 || disabled != 1 {
		t.Fatalf("enabled toggle: enabled=%d disabled=%d", enabled, disabled)
	}
}

func TestNormalizeTrayNotificationDefaults(t *testing.T) {
	title, message, kind := normalizeTrayNotification(" ", "", NotificationKind(99))
	if title != "SafeSky" {
		t.Fatalf("title=%q, want SafeSky", title)
	}
	if message == "" {
		t.Fatal("message should have default text")
	}
	if kind != NotificationInfo {
		t.Fatalf("kind=%d, want info", kind)
	}
}

func TestNotifyDoesNotPanic(t *testing.T) {
	Notify("SafeSky", "Connected", NotificationInfo)
	Notify("SafeSky", "Warning", NotificationWarning)
	Notify("SafeSky", "Error", NotificationError)
}

func TestHandleHotkeyAction(t *testing.T) {
	var enabled, opened, next int
	var profile int
	cb.OnEnable = func() { enabled++ }
	cb.OnOpen = func() { opened++ }
	cb.OnNextServer = func() { next++ }
	cb.OnProfileSwitch = func(index int) { profile = index }
	defer func() {
		cb.OnEnable = nil
		cb.OnOpen = nil
		cb.OnNextServer = nil
		cb.OnProfileSwitch = nil
	}()

	SetEnabled(false)
	handleHotkeyAction(hotkeys.ActionToggleConnection)
	handleHotkeyAction(hotkeys.ActionShowHideWindow)
	handleHotkeyAction(hotkeys.ActionNextServer)
	handleHotkeyAction(hotkeys.Action("profile_3"))

	if enabled != 1 || opened != 1 || next != 1 || profile != 3 {
		t.Fatalf("enabled=%d opened=%d next=%d profile=%d", enabled, opened, next, profile)
	}
}

func TestSetHotkeysStoresConflicts(t *testing.T) {
	conflicts := SetHotkeys(hotkeys.Settings{Enabled: true, Bindings: []hotkeys.Binding{
		{Action: hotkeys.ActionToggleConnection, Accelerator: "Ctrl+Alt+P", Enabled: true},
	}})
	if len(conflicts) != 0 {
		t.Fatalf("conflicts=%+v, want none in stub", conflicts)
	}
	got := HotkeyConflicts()
	if len(got) != 0 {
		t.Fatalf("stored conflicts=%+v, want none", got)
	}
}
