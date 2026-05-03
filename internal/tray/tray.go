// Package tray реализует системный трей SafeSky через кастомный Win32 бэкенд.
//
// Поведение иконки (в отличие от getlantern/systray):
//   - Левый клик → toggle connect/disconnect
//   - Двойной клик → перевести главное окно на передний план (OnOpen)
//   - Правый клик → показать меню
//
// Меню использует тёмную тему Windows (SetWindowTheme "DarkMode_Explorer"),
// что даёт тёмный фон на Windows 10 20H1+ и Windows 11.
package tray

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"proxyclient/internal/hotkeys"
)

// ServerItem описывает один сервер в подменю трея.
type ServerItem struct {
	ID     string
	Name   string
	Active bool   // отображается галочкой ✓
	Ping   string // опциональный пинг, например "45ms"
}

type ProfileItem struct {
	Name   string
	Active bool
}

// Callbacks — функции которые трей вызывает при действиях пользователя.
type Callbacks struct {
	OnOpen          func()
	OnEnable        func()
	OnDisable       func()
	OnCopyAddr      func(addr string)
	OnQuit          func()
	OnServerSwitch  func(serverID string)
	OnNextServer    func()
	OnProfileSwitch func(index int)
}

type HealthState int

const (
	HealthOK HealthState = iota
	HealthDegraded
	HealthCritical
)

type NotificationKind int

const (
	NotificationInfo NotificationKind = iota
	NotificationWarning
	NotificationError
)

const maxServerSlots = 10
const maxProfileSlots = 10

var (
	cb      Callbacks
	readyCh = make(chan struct{})
)

// Run запускает системный трей. Блокирует текущий поток.
// Должен вызываться из main-горутины (Win32 message loop требует конкретный поток).
func Run(callbacks Callbacks) {
	cb = callbacks
	win32Run(onReady, onExit)
}

// WaitReady блокируется пока трей не инициализирован.
func WaitReady() {
	<-readyCh
}

// SetEnabled переключает иконку и состояние пунктов меню.
func SetEnabled(enabled bool) {
	healthMu.Lock()
	state := healthState
	healthMu.Unlock()
	win32SetIconForHealth(enabled, state)
	win32SetTooltip(buildTooltip(enabled))

	win32MenuState.Lock()
	win32MenuState.enableEnabled = !enabled
	win32MenuState.disableEnabled = enabled
	win32MenuState.Unlock()
}

// SetProxyAddr обновляет адрес прокси в меню.
func SetProxyAddr(addr string) {
	win32MenuState.Lock()
	win32MenuState.copyAddr = addr
	win32MenuState.Unlock()
}

// SetActiveServer обновляет тултип трея с именем активного сервера.
// Вызывается после смены сервера чтобы тултип отражал текущий сервер
// даже если состояние enabled/disabled не менялось.
func SetActiveServer(name string) {
	serverSlotsMu.Lock()
	activeServerName = name
	serverSlotsMu.Unlock()

	// Обновляем тултип: читаем текущее состояние прокси из menu state.
	// disableEnabled=true ⟺ прокси включён (кнопка "Выключить" активна).
	win32MenuState.Lock()
	isEnabled := win32MenuState.disableEnabled
	win32MenuState.Unlock()
	win32SetTooltip(buildTooltip(isEnabled))
}

// SetServerList обновляет список серверов в подменю трея.
func SetServerList(servers []ServerItem) {
	servers = sortedServerItems(servers)
	n := len(servers)
	if n > maxServerSlots {
		n = maxServerSlots
	}
	win32MenuState.Lock()
	win32MenuState.servers = make([]ServerItem, n)
	copy(win32MenuState.servers, servers[:n])
	isEnabled := win32MenuState.disableEnabled // читаем под тем же локом
	win32MenuState.Unlock()

	// Обновляем тултип при смене списка серверов (active-флаг мог измениться).
	win32SetTooltip(buildTooltip(isEnabled))
}

func SetProfileList(profiles []ProfileItem) {
	n := len(profiles)
	if n > maxProfileSlots {
		n = maxProfileSlots
	}
	win32MenuState.Lock()
	win32MenuState.profiles = make([]ProfileItem, n)
	copy(win32MenuState.profiles, profiles[:n])
	win32MenuState.Unlock()
}

func SetActiveProfile(name string) {
	name = strings.TrimSpace(name)
	win32MenuState.Lock()
	for i := range win32MenuState.profiles {
		win32MenuState.profiles[i].Active = name != "" && win32MenuState.profiles[i].Name == name
	}
	win32MenuState.Unlock()
}

func SetTrafficSpeed(upBytesPerSec, downBytesPerSec int64) {
	win32MenuState.Lock()
	win32MenuState.speedText = fmt.Sprintf("↓ %s/s ↑ %s/s", formatTrafficBytes(downBytesPerSec), formatTrafficBytes(upBytesPerSec))
	win32MenuState.Unlock()
}

// SetBringToFront устанавливает функцию переноса окна на передний план.
// Вызывается из main.go после инициализации window.
func SetBringToFront(fn func()) {
	win32BringFront = fn
}

// SetWarming обновляет иконку/тултип трея при запуске/перезапуске sing-box.
// warming=true → тултип "SafeSky — Запуск...", warming=false → обычный тултип.
func SetWarming(warming bool) {
	warmingMu.Lock()
	warmingActive = warming
	warmingMu.Unlock()

	win32MenuState.Lock()
	isEnabled := win32MenuState.disableEnabled
	win32MenuState.Unlock()

	win32SetTooltip(buildTooltip(isEnabled))
}

func SetHealthState(state HealthState) {
	healthMu.Lock()
	healthState = state
	healthMu.Unlock()
	win32MenuState.Lock()
	isEnabled := win32MenuState.disableEnabled
	win32MenuState.Unlock()
	win32SetIconForHealth(isEnabled, state)
	win32SetTooltip(buildTooltip(isEnabled))
}

func ToggleFromTray() {
	handleTrayToggle()
}

func Notify(title, message string, kind NotificationKind) {
	title, message, kind = normalizeTrayNotification(title, message, kind)
	win32ShowNotification(title, message, kind)
}

func SetHotkeys(settings hotkeys.Settings) []hotkeys.Conflict {
	settings = hotkeys.NormalizeSettings(settings)
	hotkeyMu.Lock()
	hotkeySettings = settings
	hotkeyMu.Unlock()
	conflicts := win32ApplyHotkeys(settings)
	hotkeyMu.Lock()
	hotkeyConflicts = append([]hotkeys.Conflict(nil), conflicts...)
	hotkeyMu.Unlock()
	return conflicts
}

func HotkeyConflicts() []hotkeys.Conflict {
	hotkeyMu.Lock()
	defer hotkeyMu.Unlock()
	return append([]hotkeys.Conflict(nil), hotkeyConflicts...)
}

// ── Internal ──────────────────────────────────────────────────────────────

var (
	serverSlotsMu    sync.Mutex // защищает activeServerName
	activeServerName string

	warmingMu     sync.Mutex // защищает warmingActive
	warmingActive bool
	healthMu      sync.Mutex
	healthState   HealthState

	hotkeyMu        sync.Mutex
	hotkeySettings  hotkeys.Settings
	hotkeyConflicts []hotkeys.Conflict
)

func buildTooltip(enabled bool) string {
	warmingMu.Lock()
	warming := warmingActive
	warmingMu.Unlock()

	if warming {
		return "SafeSky — туннель запускается"
	}
	if !enabled {
		return "SafeSky — туннель выключен"
	}
	tip := "SafeSky — туннель включён"
	healthMu.Lock()
	state := healthState
	healthMu.Unlock()
	switch state {
	case HealthDegraded:
		tip += " — деградация"
	case HealthCritical:
		tip += " — проблемы соединения"
	}
	win32MenuState.Lock()
	servers := win32MenuState.servers
	win32MenuState.Unlock()

	for _, s := range servers {
		if s.Active {
			tip += "  " + s.Name
			break
		}
	}
	return tip
}

func onReady() {
	// Инициализируем иконку (выключена по умолчанию)
	win32SetIcon(false)

	win32MenuState.Lock()
	win32MenuState.enableEnabled = true
	win32MenuState.disableEnabled = false
	win32MenuState.Unlock()

	close(readyCh)
}

func onExit() {
	if cb.OnQuit != nil {
		cb.OnQuit()
	}
}

func handleTrayToggle() {
	warmingMu.Lock()
	warming := warmingActive
	warmingMu.Unlock()
	if warming {
		return
	}

	win32MenuState.Lock()
	isEnabled := win32MenuState.disableEnabled
	win32MenuState.Unlock()
	if isEnabled {
		if cb.OnDisable != nil {
			cb.OnDisable()
		}
		return
	}
	if cb.OnEnable != nil {
		cb.OnEnable()
	}
}

func handleHotkeyAction(action hotkeys.Action) {
	switch action {
	case hotkeys.ActionToggleConnection:
		handleTrayToggle()
	case hotkeys.ActionShowHideWindow:
		if cb.OnOpen != nil {
			cb.OnOpen()
		}
	case hotkeys.ActionNextServer:
		if cb.OnNextServer != nil {
			cb.OnNextServer()
		}
	default:
		if strings.HasPrefix(string(action), "profile_") && cb.OnProfileSwitch != nil {
			idx := 0
			for _, ch := range strings.TrimPrefix(string(action), "profile_") {
				if ch < '0' || ch > '9' {
					return
				}
				idx = idx*10 + int(ch-'0')
			}
			if idx > 0 {
				cb.OnProfileSwitch(idx)
			}
		}
	}
}

func sortedServerItems(servers []ServerItem) []ServerItem {
	out := append([]ServerItem(nil), servers...)
	sort.SliceStable(out, func(i, j int) bool {
		pi, okI := serverPingMS(out[i].Ping)
		pj, okJ := serverPingMS(out[j].Ping)
		if okI != okJ {
			return okI
		}
		if okI && pi != pj {
			return pi < pj
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

func trayConnectedStatusText(servers []ServerItem) string {
	for _, srv := range servers {
		if srv.Active && strings.TrimSpace(srv.Name) != "" {
			return "Туннель включён — " + srv.Name
		}
	}
	return "Туннель включён"
}

func formatTrafficBytes(bytes int64) string {
	if bytes < 0 {
		bytes = 0
	}
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	value := float64(bytes)
	units := []string{"KB", "MB", "GB", "TB"}
	for i, suffix := range units {
		value /= unit
		if value < unit || i == len(units)-1 {
			if value >= 10 {
				return fmt.Sprintf("%.0f %s", value, suffix)
			}
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return "0 B"
}

func serverPingMS(raw string) (int, bool) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	raw = strings.TrimSuffix(raw, "ms")
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	n := 0
	for _, ch := range raw {
		if ch < '0' || ch > '9' {
			return 0, false
		}
		n = n*10 + int(ch-'0')
	}
	return n, true
}

func normalizeTrayNotification(title, message string, kind NotificationKind) (string, string, NotificationKind) {
	title = strings.TrimSpace(title)
	message = strings.TrimSpace(message)
	if title == "" {
		title = "SafeSky"
	}
	if message == "" {
		message = "Состояние SafeSky обновлено."
	}
	switch kind {
	case NotificationWarning, NotificationError:
	default:
		kind = NotificationInfo
	}
	return title, message, kind
}
