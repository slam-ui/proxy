package window

import (
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

// Win32 API для управления стилем окна
var (
	user32           = windows.NewLazyDLL("user32.dll")
	getWindowLongPtr = user32.NewProc("GetWindowLongPtrW")
	setWindowLongPtr = user32.NewProc("SetWindowLongPtrW")
	setWindowPos     = user32.NewProc("SetWindowPos")
	sendMessageW     = user32.NewProc("SendMessageW")
)

const (
	gwlStyle        = -16
	wsCaption       = 0x00C00000 // заголовок окна (белая полоса)
	wsSysMenu       = 0x00080000 // системное меню
	swpNomove       = 0x0002
	swpNosize       = 0x0001
	swpNozorder     = 0x0004
	swpFrameChanged = 0x0020
	wmSysCommand    = 0x0112
	scMinimize      = 0xF020
	scMaximize      = 0xF030
)

// makeFrameless убирает системный заголовок окна (белую полосу),
// оставляя тонкую рамку для изменения размера (wsThickFrame сохраняется).
func makeFrameless(hwnd uintptr) {
	style, _, _ := getWindowLongPtr.Call(hwnd, uintptr(gwlStyle))
	style &^= wsCaption | wsSysMenu
	setWindowLongPtr.Call(hwnd, uintptr(gwlStyle), style)
	// SWP_FRAMECHANGED заставляет Windows пересчитать некликтентскую область
	setWindowPos.Call(hwnd, 0, 0, 0, 0, 0,
		swpNomove|swpNosize|swpNozorder|swpFrameChanged)
}

// Open открывает окно с Web UI. Если окно уже открыто — фокусирует его.
func Open(url string) {
	mu.Lock()
	if opened {
		mu.Unlock()
		return
	}
	opened = true
	mu.Unlock()

	go func() {
		// BUG FIX: WebView2 использует COM STA на Windows.
		// Без LockOSThread Go runtime может перемещать горутину между
		// OS-потоками, что ломает COM и приводит к зависанию окна ("Не отвечает").
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

		w.SetSize(960, 640, webview2.HintNone)

		// Убираем системную белую полосу заголовка
		hwnd := uintptr(unsafe.Pointer(w.Window()))
		makeFrameless(hwnd)

		// Привязываем JS-функции для кастомных кнопок управления окном.
		// Вызов из HTML: await window.windowMinimize() / windowClose()
		w.Bind("windowMinimize", func() {
			sendMessageW.Call(hwnd, wmSysCommand, scMinimize, 0)
		})
		w.Bind("windowMaximize", func() {
			sendMessageW.Call(hwnd, wmSysCommand, scMaximize, 0)
		})
		w.Bind("windowClose", func() {
			w.Terminate()
		})

		w.Navigate(url)
		w.Run()
	}()
}

// Close закрывает окно если оно открыто
func Close() {
	mu.Lock()
	defer mu.Unlock()
	if instance != nil {
		instance.Terminate()
	}
}
