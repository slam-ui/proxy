//go:build !windows

package tray

import "sync"

const (
	idOpen     = 1001
	idCopyAddr = 1002
	idEnable   = 1003
	idDisable  = 1004
	idQuit     = 1005
	idSrvBase  = 2000
)

var (
	win32BringFront func()

	win32MenuState struct {
		sync.Mutex
		open           bool
		enableEnabled  bool
		disableEnabled bool
		copyAddr       string
		servers        []ServerItem
	}
)

func win32Run(onReady func(), onExit func()) {
	if onReady != nil {
		onReady()
	}
	if onExit != nil {
		onExit()
	}
}

func win32SetIcon(bool)                       {}
func win32SetIconForHealth(bool, HealthState) {}
func win32SetTooltip(string)                  {}
func win32Quit()                              {}
func handleMenuCommand(id int) {
	switch id {
	case idOpen:
		if cb.OnOpen != nil {
			cb.OnOpen()
		}
	case idEnable:
		if cb.OnEnable != nil {
			cb.OnEnable()
		}
	case idDisable:
		if cb.OnDisable != nil {
			cb.OnDisable()
		}
	case idCopyAddr:
		if cb.OnCopyAddr != nil {
			win32MenuState.Lock()
			addr := win32MenuState.copyAddr
			win32MenuState.Unlock()
			cb.OnCopyAddr(addr)
		}
	case idQuit:
		if cb.OnQuit != nil {
			cb.OnQuit()
		}
	default:
		if id >= idSrvBase && cb.OnServerSwitch != nil {
			idx := id - idSrvBase
			win32MenuState.Lock()
			if idx >= 0 && idx < len(win32MenuState.servers) {
				srvID := win32MenuState.servers[idx].ID
				win32MenuState.Unlock()
				cb.OnServerSwitch(srvID)
				return
			}
			win32MenuState.Unlock()
		}
	}
}
