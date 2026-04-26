// Package tray реализует системный трей SafeSky через кастомный Win32 бэкенд.
//
// Поведение иконки (в отличие от getlantern/systray):
//   - Левый клик / двойной клик → показать меню
//   - Правый клик → перевести главное окно на передний план (OnOpen)
//
// Меню использует тёмную тему Windows (SetWindowTheme "DarkMode_Explorer"),
// что даёт тёмный фон на Windows 10 20H1+ и Windows 11.
package tray

import "sync"

// ServerItem описывает один сервер в подменю трея.
type ServerItem struct {
	ID     string
	Name   string
	Active bool   // отображается галочкой ✓
	Ping   string // опциональный пинг, например "45ms"
}

// Callbacks — функции которые трей вызывает при действиях пользователя.
type Callbacks struct {
	OnOpen         func()
	OnEnable       func()
	OnDisable      func()
	OnCopyAddr     func(addr string)
	OnQuit         func()
	OnServerSwitch func(serverID string)
}

const maxServerSlots = 10

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
	win32SetIcon(enabled)
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

// ── Internal ──────────────────────────────────────────────────────────────

var (
	serverSlotsMu    sync.Mutex // защищает activeServerName
	activeServerName string

	warmingMu     sync.Mutex // защищает warmingActive
	warmingActive bool
)

func buildTooltip(enabled bool) string {
	warmingMu.Lock()
	warming := warmingActive
	warmingMu.Unlock()

	if warming {
		return "SafeSky — Запуск..."
	}
	if !enabled {
		return "SafeSky — Выключен"
	}
	tip := "SafeSky — Включён"
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
