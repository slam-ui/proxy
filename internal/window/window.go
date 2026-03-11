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

// Win32 / DWM API
var (
	user32           = windows.NewLazyDLL("user32.dll")
	dwmapi           = windows.NewLazyDLL("dwmapi.dll")
	getWindowLongPtr = user32.NewProc("GetWindowLongPtrW")
	setWindowLongPtr = user32.NewProc("SetWindowLongPtrW")
	setWindowPos     = user32.NewProc("SetWindowPos")
	sendMessageW     = user32.NewProc("SendMessageW")
	// DwmExtendFrameIntoClientArea позволяет убрать DWM-рамку полностью.
	// Без него DWM рисует тонкую светлую полосу сверху даже после снятия WS_CAPTION.
	dwmExtendFrame = dwmapi.NewProc("DwmExtendFrameIntoClientArea")
)

// gwlStyle = -16 как uintptr через bitwise complement.
// ^uintptr(15) == 0xFFFF...FFF0 (two's complement -16).
// Go запрещает uintptr(-16) как константу, но ^uintptr(15) — валидно.
const gwlStyle = ^uintptr(15) // == -16

const (
	wsCaption       = uintptr(0x00C00000) // заголовок окна (белая полоса)
	wsSysMenu       = uintptr(0x00080000) // системное меню
	swpNomove       = uintptr(0x0002)
	swpNosize       = uintptr(0x0001)
	swpNozorder     = uintptr(0x0004)
	swpFrameChanged = uintptr(0x0020)
	wmSysCommand    = uintptr(0x0112)
	scMinimize      = uintptr(0xF020)
	scMaximize      = uintptr(0xF030)
)

// dwmMargins для DwmExtendFrameIntoClientArea.
// Значения -1 говорят DWM полностью не рисовать никакой рамки.
type dwmMargins struct{ Left, Right, Top, Bottom int32 }

// makeFrameless убирает системный заголовок и DWM-рамку.
//
// Двухшаговый процесс:
//  1. SetWindowLongPtr — снимаем WS_CAPTION и WS_SYSMENU со стиля окна,
//     чтобы Win32 перестал резервировать место под заголовок.
//  2. DwmExtendFrameIntoClientArea с margins {-1,-1,-1,-1} — говорим DWM
//     расширить клиентскую область на весь размер окна и не рисовать свою рамку.
//     Без этого шага DWM всё равно рисует тонкую светлую полосу сверху.
func makeFrameless(hwnd uintptr) {
	// Шаг 1: убираем WS_CAPTION | WS_SYSMENU
	style, _, _ := getWindowLongPtr.Call(hwnd, gwlStyle)
	style &^= wsCaption | wsSysMenu
	setWindowLongPtr.Call(hwnd, gwlStyle, style)
	setWindowPos.Call(hwnd, 0, 0, 0, 0, 0,
		swpNomove|swpNosize|swpNozorder|swpFrameChanged)

	// Шаг 2: убираем DWM-рамку через отрицательные margins
	m := dwmMargins{-1, -1, -1, -1}
	dwmExtendFrame.Call(hwnd, uintptr(unsafe.Pointer(&m)))
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

		// Убираем заголовок и DWM-рамку
		hwnd := uintptr(unsafe.Pointer(w.Window()))
		makeFrameless(hwnd)

		// JS-биндинги для кастомных кнопок управления окном.
		// Вызов из HTML: await windowMinimize() / windowMaximize() / windowClose()
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
