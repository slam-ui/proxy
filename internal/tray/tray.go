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
	readyCh  = make(chan struct{}) // закрывается когда onReady завершил инициализацию
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

// SetEnabled меняет иконку и состояние пунктов меню.
// Вызывать только после WaitReady() или внутри onReady.
func SetEnabled(enabled bool) {
	// Защита на случай вызова до onReady (не должна срабатывать если используется WaitReady).
	if mEnable == nil || mDisable == nil {
		return
	}

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

	// Сигнализируем WaitReady: все пункты меню инициализированы
	close(readyCh)

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
