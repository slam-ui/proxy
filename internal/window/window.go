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
	postMessageW       = user32.NewProc("PostMessageW")
	getWindowPlacement = user32.NewProc("GetWindowPlacement")
	getAncestor        = user32.NewProc("GetAncestor")
	dwmSetAttr         = dwmAPI.NewProc("DwmSetWindowAttribute")
)

const (
	swpNozorder      = 0x0004
	swpNoActivate    = 0x0010
	wmSysCommand     = 0x0112
	wmClose          = 0x0010
	scMinimize       = 0xF020
	scMaximize       = 0xF030
	scRestore        = 0xF120
	showStateMaximized = 3

	// DWM атрибуты для стилизации нативного заголовка
	dwmwaImmersiveDarkMode = 20 // BOOL: 1 = тёмный режим
	dwmwaCaptionColor      = 35 // COLORREF: цвет полосы заголовка
	dwmwaTextColor         = 36 // COLORREF: цвет текста заголовка
	dwmwaBorderColor       = 34 // COLORREF: цвет рамки
)

// colorref конвертирует #RRGGBB в Windows COLORREF (0x00BBGGRR).
func colorref(r, g, b uint32) uint32 {
	return b<<16 | g<<8 | r
}

// applyDarkTitle красит нативный заголовок под цветовую схему приложения.
func applyDarkTitle(hwnd uintptr) {
	// Тёмный режим (убирает белый фон системных кнопок)
	dark := uint32(1)
	dwmSetAttr.Call(hwnd, dwmwaImmersiveDarkMode, uintptr(unsafe.Pointer(&dark)), 4)

	// --surface: #13131e → COLORREF
	capColor := colorref(0x13, 0x13, 0x1e)
	dwmSetAttr.Call(hwnd, dwmwaCaptionColor, uintptr(unsafe.Pointer(&capColor)), 4)

	// Текст заголовка: #6a6a8a (--muted2), ненавязчивый
	textColor := colorref(0x6a, 0x6a, 0x8a)
	dwmSetAttr.Call(hwnd, dwmwaTextColor, uintptr(unsafe.Pointer(&textColor)), 4)

	// Рамка: #1f1f30 (--border)
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

		// Красим нативный заголовок под UI — НЕ убираем wsCaption,
		// чтобы Windows сам обрабатывал перетаскивание.
		applyDarkTitle(rootHwnd)

		// Восстанавливаем позицию из прошлой сессии.
		if s, ok := loadState(); ok {
			setWindowPos.Call(rootHwnd, 0,
				uintptr(s.X), uintptr(s.Y),
				uintptr(s.Width), uintptr(s.Height),
				swpNozorder|swpNoActivate)
		} else {
			w.SetSize(960, 640, webview2.HintNone)
		}

		// JS биндинги для кастомных кнопок в HTML
		// (нативные кнопки заголовка тоже работают параллельно)
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

		saveState(rootHwnd)
	}()
}

// Close закрывает окно если оно открыто.
func Close() {
	mu.Lock()
	defer mu.Unlock()
	if instance != nil {
		instance.Terminate()
	}
}