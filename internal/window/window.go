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
	dwmExtendFrame   = dwmapi.NewProc("DwmExtendFrameIntoClientArea")
	dwmSetWindowAttr = dwmapi.NewProc("DwmSetWindowAttribute")
)

// gwlStyle = -16 как uintptr через bitwise complement.
// ^uintptr(15) == 0xFFFF...FFF0 (two's complement -16).
const gwlStyle = ^uintptr(15) // == -16

const (
	wsCaption       = uintptr(0x00C00000)
	wsSysMenu       = uintptr(0x00080000)
	swpNomove       = uintptr(0x0002)
	swpNosize       = uintptr(0x0001)
	swpNozorder     = uintptr(0x0004)
	swpFrameChanged = uintptr(0x0020)
	wmSysCommand    = uintptr(0x0112)
	scMinimize      = uintptr(0xF020)
	scMaximize      = uintptr(0xF030)

	// DwmSetWindowAttribute: отключает отрисовку неклиентской области DWM
	dwmwaNCRenderingPolicy = uintptr(2) // DWMWA_NCRENDERING_POLICY
	dwmncrpDisabled        = uintptr(1) // DWMNCRP_DISABLED
)

// dwmMargins для DwmExtendFrameIntoClientArea.
// {0,0,0,0} — сворачивает DWM-рамку, не добавляя «стекло» по периметру.
type dwmMargins struct{ Left, Right, Top, Bottom int32 }

// makeFrameless убирает системный заголовок и DWM-рамку без артефактов.
//
// Три шага:
//  1. SetWindowLongPtr — снимаем WS_CAPTION | WS_SYSMENU; Win32 перестаёт
//     резервировать место под заголовок.
//  2. DwmSetWindowAttribute(DWMWA_NCRENDERING_POLICY, DWMNCRP_DISABLED) —
//     отключает отрисовку неклиентской области DWM (убирает белую/серую полосу
//     которую DWM рисует поверх окна независимо от стилей Win32).
//  3. DwmExtendFrameIntoClientArea({0,0,0,0}) — сворачивает DWM-рамку в ноль.
//     НЕ {-1,-1,-1,-1}: отрицательные margins включают режим "sheet of glass"
//     и добавляют рамку по всему периметру.
func makeFrameless(hwnd uintptr) {
	// Шаг 1: убираем Win32 заголовок
	style, _, _ := getWindowLongPtr.Call(hwnd, gwlStyle)
	style &^= wsCaption | wsSysMenu
	setWindowLongPtr.Call(hwnd, gwlStyle, style)
	setWindowPos.Call(hwnd, 0, 0, 0, 0, 0,
		swpNomove|swpNosize|swpNozorder|swpFrameChanged)

	// Шаг 2: отключаем DWM некlientскую отрисовку
	policy := dwmncrpDisabled
	dwmSetWindowAttr.Call(hwnd, dwmwaNCRenderingPolicy,
		uintptr(unsafe.Pointer(&policy)), unsafe.Sizeof(policy))

	// Шаг 3: сворачиваем DWM рамку в ноль
	m := dwmMargins{0, 0, 0, 0}
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
