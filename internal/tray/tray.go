package tray

import (
	"github.com/getlantern/systray"
)

// Callbacks — функции которые трей вызывает при действиях пользователя
type Callbacks struct {
	OnOpen    func()
	OnEnable  func()
	OnDisable func()
	OnQuit    func()
}

var (
	mOpen    *systray.MenuItem
	mEnable  *systray.MenuItem
	mDisable *systray.MenuItem
	mQuit    *systray.MenuItem
	cb       Callbacks
)

// Run запускает системный трей. Блокирует текущий поток (должен вызываться из main).
func Run(callbacks Callbacks) {
	cb = callbacks
	systray.Run(onReady, onExit)
}

// SetEnabled меняет иконку и состояние пунктов меню
func SetEnabled(enabled bool) {
	isOn = enabled
	if enabled {
		systray.SetIcon(iconOn())
		systray.SetTooltip("Proxy — включён")
		mEnable.Disable()
		mDisable.Enable()
	} else {
		systray.SetIcon(iconOff())
		systray.SetTooltip("Proxy — выключен")
		mEnable.Enable()
		mDisable.Disable()
	}
}

func onReady() {
	systray.SetIcon(iconOff())
	systray.SetTitle("Proxy")
	systray.SetTooltip("Proxy Control")

	mOpen = systray.AddMenuItem("Открыть панель", "Открыть Web UI")
	systray.AddSeparator()
	mEnable = systray.AddMenuItem("Включить", "Включить прокси")
	mDisable = systray.AddMenuItem("Выключить", "Выключить прокси")
	mDisable.Disable()
	systray.AddSeparator()
	mQuit = systray.AddMenuItem("Выход", "Завершить приложение")

	// Обработка кликов
	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				if cb.OnOpen != nil {
					cb.OnOpen()
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
