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
	getSystemMetrics = user32.NewProc("GetSystemMetrics")
	dwmSetWindowAttr = dwmapi.NewProc("DwmSetWindowAttribute")
	dwmExtendFrame   = dwmapi.NewProc("DwmExtendFrameIntoClientArea")
)

// gwlStyle = -16 через bitwise complement: ^uintptr(15) == -16 в two's complement
const gwlStyle = ^uintptr(15)

const (
	// Убираем только заголовок — WS_THICKFRAME оставляем для resize!
	wsCaption = uintptr(0x00C00000) // заголовок (белая полоса)
	wsSysMenu = uintptr(0x00080000) // системное меню
	// wsThickFrame НЕ убираем — он даёт resize по краям окна

	swpNomove       = uintptr(0x0002)
	swpNosize       = uintptr(0x0001)
	swpNozorder     = uintptr(0x0004)
	swpFrameChanged = uintptr(0x0020)

	wmSysCommand = uintptr(0x0112)
	scMinimize   = uintptr(0xF020)
	scMaximize   = uintptr(0xF030)

	// DWM: отключаем отрисовку неклиентской области (белая полоса от DWM)
	dwmwaNCRenderingPolicy = uintptr(2) // DWMWA_NCRENDERING_POLICY
	dwmncrpDisabled        = uintptr(1) // DWMNCRP_DISABLED

	// GetSystemMetrics индексы
	smCxScreen = uintptr(0) // ширина экрана
	smCyScreen = uintptr(1) // высота экрана
)

type dwmMargins struct{ Left, Right, Top, Bottom int32 }

// makeFrameless убирает заголовок, оставляя resize по краям.
//
// Убираем только WS_CAPTION и WS_SYSMENU.
// WS_THICKFRAME ОСТАВЛЯЕМ — он нужен для resize окна мышью.
// Белая полоса от DWM убирается через DwmSetWindowAttribute(DWMNCRP_DISABLED).
func makeFrameless(hwnd uintptr) {
	style, _, _ := getWindowLongPtr.Call(hwnd, gwlStyle)
	style &^= wsCaption | wsSysMenu
	setWindowLongPtr.Call(hwnd, gwlStyle, style)
	setWindowPos.Call(hwnd, 0, 0, 0, 0, 0,
		swpNomove|swpNosize|swpNozorder|swpFrameChanged)

	// Отключаем DWM неклиентскую отрисовку — убирает белую полосу сверху
	policy := dwmncrpDisabled
	dwmSetWindowAttr.Call(hwnd, dwmwaNCRenderingPolicy,
		uintptr(unsafe.Pointer(&policy)), unsafe.Sizeof(policy))

	// Сворачиваем DWM рамку в ноль (0 = убрать, -1 = "sheet of glass" — не то)
	m := dwmMargins{0, 0, 0, 0}
	dwmExtendFrame.Call(hwnd, uintptr(unsafe.Pointer(&m)))
}

// screenSize возвращает размер основного монитора
func screenSize() (int, int) {
	w, _, _ := getSystemMetrics.Call(smCxScreen)
	h, _, _ := getSystemMetrics.Call(smCyScreen)
	return int(w), int(h)
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
		// BUG FIX: WebView2 использует COM STA — LockOSThread обязателен
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

		// Размер окна: экран минус отступ 60px с каждой стороны
		sw, sh := screenSize()
		margin := 60
		winW := sw - margin*2
		winH := sh - margin*2
		if winW < 800 {
			winW = 800
		}
		if winH < 600 {
			winH = 600
		}
		w.SetSize(winW, winH, webview2.HintNone)

		hwnd := uintptr(unsafe.Pointer(w.Window()))
		makeFrameless(hwnd)

		// Центрируем окно на экране
		x := (sw - winW) / 2
		y := (sh - winH) / 2
		setWindowPos.Call(hwnd, 0,
			uintptr(x), uintptr(y), uintptr(winW), uintptr(winH),
			swpNozorder|swpFrameChanged)

		// JS-биндинги для кастомных кнопок в HTML
		// Вызов: await windowMinimize() / windowMaximize() / windowClose()
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
