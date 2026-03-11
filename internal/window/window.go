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

var (
	user32           = windows.NewLazyDLL("user32.dll")
	dwmapi           = windows.NewLazyDLL("dwmapi.dll")
	getWindowLongPtr = user32.NewProc("GetWindowLongPtrW")
	setWindowLongPtr = user32.NewProc("SetWindowLongPtrW")
	setWindowPos     = user32.NewProc("SetWindowPos")
	sendMessageW     = user32.NewProc("SendMessageW")
	dwmExtendFrame   = dwmapi.NewProc("DwmExtendFrameIntoClientArea")
	dwmSetWindowAttr = dwmapi.NewProc("DwmSetWindowAttribute")
)

// gwlStyle = -16 через bitwise complement: ^uintptr(15) == -16 в two's complement
const gwlStyle = ^uintptr(15)

const (
	// Биты стиля окна которые создают заголовок и рамку
	wsCaption    = uintptr(0x00C00000) // заголовок (белая полоса)
	wsSysMenu    = uintptr(0x00080000) // системное меню
	wsThickFrame = uintptr(0x00040000) // рамка изменения размера
	wsBorder     = uintptr(0x00800000) // тонкая рамка

	swpNomove       = uintptr(0x0002)
	swpNosize       = uintptr(0x0001)
	swpNozorder     = uintptr(0x0004)
	swpFrameChanged = uintptr(0x0020)

	wmSysCommand = uintptr(0x0112)
	scMinimize   = uintptr(0xF020)
	scMaximize   = uintptr(0xF030)

	dwmwaNCRenderingPolicy = uintptr(2) // DWMWA_NCRENDERING_POLICY
	dwmncrpDisabled        = uintptr(1) // DWMNCRP_DISABLED — DWM не рисует рамку
)

type dwmMargins struct{ Left, Right, Top, Bottom int32 }

// makeFrameless полностью убирает системную рамку и заголовок.
//
// Нужно убрать ВСЕ четыре бита отвечающих за рамки:
//
//	WS_CAPTION    — заголовок (белая/серая полоса сверху)
//	WS_SYSMENU    — системное меню в заголовке
//	WS_THICKFRAME — рамка изменения размера по периметру
//	WS_BORDER     — тонкая рамка окна
//
// Если убрать только WS_CAPTION, Windows всё равно рисует рамку через
// WS_THICKFRAME и DWM добавляет тень/border поверх.
func makeFrameless(hwnd uintptr) {
	style, _, _ := getWindowLongPtr.Call(hwnd, gwlStyle)
	style &^= wsCaption | wsSysMenu | wsThickFrame | wsBorder
	setWindowLongPtr.Call(hwnd, gwlStyle, style)
	setWindowPos.Call(hwnd, 0, 0, 0, 0, 0,
		swpNomove|swpNosize|swpNozorder|swpFrameChanged)

	// Отключаем DWM некlientскую отрисовку — убирает DWM-рамку/тень
	policy := dwmncrpDisabled
	dwmSetWindowAttr.Call(hwnd, dwmwaNCRenderingPolicy,
		uintptr(unsafe.Pointer(&policy)), unsafe.Sizeof(policy))

	// Сворачиваем DWM рамку в ноль (НЕ -1: отрицательные margins = "sheet of glass")
	m := dwmMargins{0, 0, 0, 0}
	dwmExtendFrame.Call(hwnd, uintptr(unsafe.Pointer(&m)))
}

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

		w.SetSize(960, 640, webview2.HintNone)

		hwnd := uintptr(unsafe.Pointer(w.Window()))
		makeFrameless(hwnd)

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

func Close() {
	mu.Lock()
	defer mu.Unlock()
	if instance != nil {
		instance.Terminate()
	}
}
