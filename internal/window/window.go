package window

import (
	"encoding/json"
	"os"
	"runtime"
	"sync"
	"unsafe"

	"github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

var (
	mu       sync.Mutex
	instance webview2.WebView
	opened   bool
)

var (
	user32             = windows.NewLazyDLL("user32.dll")
	dwmAPI             = windows.NewLazyDLL("dwmapi.dll")
	setWindowPos       = user32.NewProc("SetWindowPos")
	getWindowRect      = user32.NewProc("GetWindowRect")
	showWindow         = user32.NewProc("ShowWindow")
	postMessageW       = user32.NewProc("PostMessageW")
	getWindowPlacement = user32.NewProc("GetWindowPlacement")
	getAncestor        = user32.NewProc("GetAncestor")
	dwmSetAttr         = dwmAPI.NewProc("DwmSetWindowAttribute")
)

const (
	swpNozorder    = 0x0004
	swpNoActivate  = 0x0010
	swpShowWindow  = 0x0040
	wmSysCommand   = 0x0112
	wmClose        = 0x0010
	scMinimize     = 0xF020
	scMaximize     = 0xF030
	scRestore      = 0xF120
	swShow         = 5  // ShowWindow: show at current pos/size
	swRestore      = 9  // ShowWindow: restore from minimized/maximized
	showStateMaximized = 3

	// DWM атрибуты для стилизации нативного заголовка
	dwmwaImmersiveDarkMode = 20
	dwmwaCaptionColor      = 35
	dwmwaTextColor         = 36
	dwmwaBorderColor       = 34
)

func colorref(r, g, b uint32) uint32 { return b<<16 | g<<8 | r }

func applyDarkTitle(hwnd uintptr) {
	dark := uint32(1)
	dwmSetAttr.Call(hwnd, dwmwaImmersiveDarkMode, uintptr(unsafe.Pointer(&dark)), 4)
	capColor := colorref(0x13, 0x13, 0x1e)
	dwmSetAttr.Call(hwnd, dwmwaCaptionColor, uintptr(unsafe.Pointer(&capColor)), 4)
	textColor := colorref(0x6a, 0x6a, 0x8a)
	dwmSetAttr.Call(hwnd, dwmwaTextColor, uintptr(unsafe.Pointer(&textColor)), 4)
	borderColor := colorref(0x1f, 0x1f, 0x30)
	dwmSetAttr.Call(hwnd, dwmwaBorderColor, uintptr(unsafe.Pointer(&borderColor)), 4)
}

// windowState — позиция и размер окна между запусками.
type windowState struct {
	X, Y, Width, Height int32
}

const statePath = "window_state.json"

func loadState() (windowState, bool) {
	data, err := os.ReadFile(statePath)
	if err != nil {
		return windowState{}, false
	}
	var s windowState
	if json.Unmarshal(data, &s) != nil {
		return windowState{}, false
	}
	if s.Width < 400 || s.Height < 300 {
		return windowState{}, false
	}
	return s, true
}

func saveState(hwnd uintptr) {
	var r [4]int32
	getWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r[0])))
	s := windowState{X: r[0], Y: r[1], Width: r[2] - r[0], Height: r[3] - r[1]}
	data, _ := json.Marshal(s)
	_ = os.WriteFile(statePath, data, 0644)
}

func isZoomed(hwnd uintptr) bool {
	var wp [12]uint32
	wp[0] = uint32(unsafe.Sizeof(wp))
	getWindowPlacement.Call(hwnd, uintptr(unsafe.Pointer(&wp[0])))
	return wp[2] == showStateMaximized
}

// Open открывает окно с Web UI.
// При повторном вызове (через трей) создаёт новое окно с сохранёнными размерами.
func Open(url string) {
	mu.Lock()
	if opened {
		mu.Unlock()
		return
	}
	opened = true
	mu.Unlock()

	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		w := webview2.NewWithOptions(webview2.WebViewOptions{
			Debug:  false,
			Window: nil,
		})
		if w == nil {
			mu.Lock()
			opened = false
			mu.Unlock()
			return
		}
		defer func() {
			w.Destroy()
			mu.Lock()
			opened = false
			instance = nil
			mu.Unlock()
		}()

		mu.Lock()
		instance = w
		mu.Unlock()

		childHwnd := uintptr(unsafe.Pointer(w.Window()))
		rootHwnd, _, _ := getAncestor.Call(childHwnd, 2) // GA_ROOT
		if rootHwnd == 0 {
			rootHwnd = childHwnd
		}

		applyDarkTitle(rootHwnd)

		// Устанавливаем размер/позицию ДО Navigate и ДО первого показа окна.
		// Без этого пользователь видит вспышку: окно появляется с дефолтным
		// размером, потом мгновенно перепрыгивает на сохранённый.
		if s, ok := loadState(); ok {
			// SWP_NOZORDER | SWP_NOACTIVATE | SWP_SHOWWINDOW — перемещаем
			// и показываем атомарно, без промежуточного дефолтного состояния.
			setWindowPos.Call(rootHwnd, 0,
				uintptr(s.X), uintptr(s.Y),
				uintptr(s.Width), uintptr(s.Height),
				swpNozorder|swpShowWindow)
		} else {
			// Первый запуск: дефолт 960×640, показываем в центре экрана.
			w.SetSize(960, 640, webview2.HintNone)
			showWindow.Call(rootHwnd, swShow)
		}

		w.Bind("windowMinimize", func() {
			postMessageW.Call(rootHwnd, wmSysCommand, scMinimize, 0)
		})
		w.Bind("windowMaximize", func() {
			if isZoomed(rootHwnd) {
				postMessageW.Call(rootHwnd, wmSysCommand, scRestore, 0)
			} else {
				postMessageW.Call(rootHwnd, wmSysCommand, scMaximize, 0)
			}
		})
		w.Bind("windowClose", func() {
			postMessageW.Call(rootHwnd, wmClose, 0, 0)
		})

		w.Navigate(url)
		w.Run()

		// w.Run() вернулся — окно закрыто. Сохраняем текущий размер/позицию.
		saveState(rootHwnd)
	}()
}

// Close принудительно закрывает окно (вызывается при выходе из приложения).
func Close() {
	mu.Lock()
	defer mu.Unlock()
	if instance != nil {
		instance.Terminate()
	}
}
