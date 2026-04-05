package tray

import (
	"sync"

	"github.com/getlantern/systray"
)

// ServerItem описывает один сервер в подменю трея.
// C-6: аналог Clash Verge Rev — список серверов с активным маркером и пингом.
type ServerItem struct {
	ID     string
	Name   string
	Active bool   // отображается галочкой ✓
	Ping   string // опциональный пинг, например "45ms"
}

// Callbacks — функции которые трей вызывает при действиях пользователя
type Callbacks struct {
	OnOpen         func()
	OnEnable       func()
	OnDisable      func()
	OnCopyAddr     func(addr string) // копировать адрес прокси в буфер обмена
	OnQuit         func()
	OnServerSwitch func(serverID string) // C-6: переключить активный сервер
}

const maxServerSlots = 10 // C-6: максимум серверов в подменю трея

var (
	mOpen     *systray.MenuItem
	mEnable   *systray.MenuItem
	mDisable  *systray.MenuItem
	mCopyAddr *systray.MenuItem
	mQuit     *systray.MenuItem

	// C-6: подменю серверов
	mServersParent *systray.MenuItem
	serverSlots    [maxServerSlots]*systray.MenuItem

	cb        Callbacks
	readyCh   = make(chan struct{})
	proxyAddr string // адрес прокси для копирования

	// C-6: текущие ID серверов в слотах и имя активного сервера
	serverSlotsMu    sync.Mutex
	serverSlotIDs    [maxServerSlots]string
	activeServerName string
	proxyEnabled     bool // для построения тултипа
)

// Run запускает системный трей. Блокирует текущий поток (должен вызываться из main).
func Run(callbacks Callbacks) {
	cb = callbacks
	systray.Run(onReady, onExit)
}

// WaitReady блокируется до тех пор, пока onReady не инициализирует пункты меню.
// Вызывать перед первым SetEnabled — иначе вызов будет тихо проигнорирован.
func WaitReady() {
	<-readyCh
}

// SetProxyAddr обновляет адрес прокси в меню трея.
// Вызывать после WaitReady().
func SetProxyAddr(addr string) {
	proxyAddr = addr
	if mCopyAddr != nil {
		if addr != "" {
			mCopyAddr.SetTitle("Копировать адрес  " + addr)
			mCopyAddr.Enable()
		} else {
			mCopyAddr.SetTitle("Копировать адрес")
			mCopyAddr.Disable()
		}
	}
	if proxyEnabled {
		setTooltip(true)
	}
}

// SetEnabled меняет иконку и состояние пунктов меню.
// Вызывать только после WaitReady() или внутри onReady.
func SetEnabled(enabled bool) {
	proxyEnabled = enabled
	// Защита на случай вызова до onReady (не должна срабатывать если используется WaitReady).
	if mEnable == nil || mDisable == nil {
		return
	}

	if enabled {
		systray.SetIcon(iconOn())
		setTooltip(true)
		mEnable.Disable()
		mDisable.Enable()
	} else {
		systray.SetIcon(iconOff())
		systray.SetTooltip("Proxy — выключен")
		mEnable.Enable()
		mDisable.Disable()
	}
}

// C-6: SetActiveServer обновляет тултип с именем активного сервера.
// Вызывать после успешного применения конфига.
func SetActiveServer(name string) {
	serverSlotsMu.Lock()
	activeServerName = name
	serverSlotsMu.Unlock()
	if proxyEnabled {
		setTooltip(true)
	}
}

// C-6: SetServerList обновляет динамическое подменю серверов (до maxServerSlots).
// Лишние серверы (>10) отображаются как «Открыть панель...» в UI.
// Потокобезопасен — можно вызывать из любой горутины.
func SetServerList(servers []ServerItem) {
	if mServersParent == nil {
		return
	}

	n := len(servers)
	if n > maxServerSlots {
		n = maxServerSlots
	}

	serverSlotsMu.Lock()
	defer serverSlotsMu.Unlock()

	for i := 0; i < maxServerSlots; i++ {
		if serverSlots[i] == nil {
			continue
		}
		if i < n {
			s := servers[i]
			serverSlotIDs[i] = s.ID
			title := buildSlotTitle(s)
			serverSlots[i].SetTitle(title)
			serverSlots[i].Show()
		} else {
			serverSlotIDs[i] = ""
			serverSlots[i].Hide()
		}
	}
}

// buildSlotTitle формирует заголовок пункта меню для сервера.
// Активный помечается «✓ », неактивный — «  » для выравнивания.
func buildSlotTitle(s ServerItem) string {
	prefix := "  "
	if s.Active {
		prefix = "✓ "
	}
	title := prefix + s.Name
	if s.Ping != "" {
		title += " (" + s.Ping + ")"
	}
	return title
}

// setTooltip выставляет тултип трея с учётом имени активного сервера и адреса прокси.
func setTooltip(enabled bool) {
	if !enabled {
		systray.SetTooltip("Proxy — выключен")
		return
	}
	serverSlotsMu.Lock()
	name := activeServerName
	serverSlotsMu.Unlock()

	tooltip := "Proxy — включён"
	if name != "" {
		tooltip += "  " + name
	}
	if proxyAddr != "" {
		tooltip += "  " + proxyAddr
	}
	systray.SetTooltip(tooltip)
}

func onReady() {
	systray.SetIcon(iconOff())
	systray.SetTitle("Proxy")
	systray.SetTooltip("Proxy Control")

	mOpen = systray.AddMenuItem("Открыть панель", "Открыть Web UI")
	mCopyAddr = systray.AddMenuItem("Копировать адрес", "Скопировать адрес прокси в буфер")
	mCopyAddr.Disable()
	systray.AddSeparator()
	mEnable = systray.AddMenuItem("Включить", "Включить прокси")
	mDisable = systray.AddMenuItem("Выключить", "Выключить прокси")
	mDisable.Disable()

	// C-6: динамическое подменю серверов (до maxServerSlots слотов)
	systray.AddSeparator()
	mServersParent = systray.AddMenuItem("Серверы ▶", "Выбрать сервер")
	for i := 0; i < maxServerSlots; i++ {
		i := i // захват для горутины
		serverSlots[i] = mServersParent.AddSubMenuItem("", "")
		serverSlots[i].Hide()
		go func() {
			for range serverSlots[i].ClickedCh {
				serverSlotsMu.Lock()
				id := serverSlotIDs[i]
				serverSlotsMu.Unlock()
				if id != "" && cb.OnServerSwitch != nil {
					cb.OnServerSwitch(id)
				}
			}
		}()
	}

	systray.AddSeparator()
	mQuit = systray.AddMenuItem("Выход", "Завершить приложение")

	// Сигнализируем WaitReady: все пункты меню инициализированы
	if proxyAddr != "" {
		mCopyAddr.SetTitle("Копировать адрес  " + proxyAddr)
		mCopyAddr.Enable()
	} else {
		mCopyAddr.SetTitle("Копировать адрес")
		mCopyAddr.Disable()
	}
	if proxyEnabled {
		setTooltip(true)
	}
	close(readyCh)

	// Обработка кликов основных пунктов
	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				if cb.OnOpen != nil {
					cb.OnOpen()
				}
			case <-mCopyAddr.ClickedCh:
				if cb.OnCopyAddr != nil {
					cb.OnCopyAddr(proxyAddr)
				}
			case <-mEnable.ClickedCh:
				if cb.OnEnable != nil {
					cb.OnEnable()
				}
			case <-mDisable.ClickedCh:
				if cb.OnDisable != nil {
					cb.OnDisable()
				}
			case <-mQuit.ClickedCh:
				systray.Quit()
			}
		}
	}()
}

func onExit() {
	if cb.OnQuit != nil {
		cb.OnQuit()
	}
}
