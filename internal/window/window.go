package window

import (
	"encoding/json"
	"os"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"proxyclient/internal/fileutil"

	"github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

var (
	mu       sync.Mutex
	instance webview2.WebView
	opened   bool
)

var (
	user32                = windows.NewLazyDLL("user32.dll")
	dwmAPI                = windows.NewLazyDLL("dwmapi.dll")
	kernel32dll           = windows.NewLazyDLL("kernel32.dll")
	setWindowPos          = user32.NewProc("SetWindowPos")
	getWindowRect         = user32.NewProc("GetWindowRect")
	postMessageW          = user32.NewProc("PostMessageW")
	sendMessageW          = user32.NewProc("SendMessageW")
	getWindowPlacement    = user32.NewProc("GetWindowPlacement")
	getAncestor           = user32.NewProc("GetAncestor")
	isWindow              = user32.NewProc("IsWindow")
	dwmSetAttr            = dwmAPI.NewProc("DwmSetWindowAttribute")
	getWindowLong         = user32.NewProc("GetWindowLongW")
	setWindowLong         = user32.NewProc("SetWindowLongW")
	releaseCapture        = user32.NewProc("ReleaseCapture")
	showWindowProc        = user32.NewProc("ShowWindow")
	setForegroundWindowFn = user32.NewProc("SetForegroundWindow")
	bringWindowToTopFn    = user32.NewProc("BringWindowToTop")
	isIconic              = user32.NewProc("IsIconic")
	loadImageW            = user32.NewProc("LoadImageW")
	pGetModuleHandle      = kernel32dll.NewProc("GetModuleHandleW")
)

const (
	swRestore          = 9
	swShow             = 5
	wmSetIcon          = 0x0080
	iconBig            = 1
	iconSmall          = 0
	imageIcon          = 1
	lrDefaultSize      = 0x0040
	lrShared           = 0x8000
	swpNozorder        = 0x0004
	swpNoActivate      = 0x0010
	swpNoSize          = 0x0001
	swpFrameChanged    = 0x0020
	wmSysCommand       = 0x0112
	wmClose            = 0x0010
	wmNcLButtonDown    = 0x00A1
	htCaption          = 2
	scMinimize         = 0xF020
	scMaximize         = 0xF030
	scRestore          = 0xF120
	showStateMaximized = 3
	wsCaption          = 0x00C00000 // WS_CAPTION

	dwmwaImmersiveDarkMode  = 20
	dwmwaCaptionColor       = 35
	dwmwaTextColor          = 36
	dwmwaBorderColor        = 34
	dwmwaSystemBackdropType = 38 // Windows 11 22H2+: 2=Mica, 3=Acrylic, 4=Tabbed
	dwmwaWindowCornerPref   = 33 // 2=ROUND, 3=ROUNDSMALL
)

type dwmSystemBackdrop uint32

const (
	backdropNone    dwmSystemBackdrop = 1
	backdropMica    dwmSystemBackdrop = 2
	backdropAcrylic dwmSystemBackdrop = 3
	backdropTabbed  dwmSystemBackdrop = 4
)

func colorref(r, g, b uint32) uint32 { return b<<16 | g<<8 | r }

// setAppIcon загружает иконку из ресурсов .exe (ID=1, вставляется goversioninfo)
// и устанавливает её на окно — обновляет иконку в панели задач и в titlebar.
func setAppIcon(hwnd uintptr) {
	hMod, _, _ := pGetModuleHandle.Call(0) // NULL → текущий .exe
	if hMod == 0 {
		return
	}
	// Большая иконка (32×32) — панель задач, Alt+Tab
	hBig, _, _ := loadImageW.Call(
		hMod,
		1, // MAKEINTRESOURCE(1) — goversioninfo всегда кладёт иконку с ID=1
		imageIcon,
		0, 0, // 0,0 → системный размер по умолчанию
		lrDefaultSize|lrShared,
	)
	// Маленькая иконка (16×16) — titlebar
	hSmall, _, _ := loadImageW.Call(
		hMod,
		1,
		imageIcon,
		16, 16,
		lrShared,
	)
	if hBig != 0 {
		sendMessageW.Call(hwnd, wmSetIcon, iconBig, hBig)
	}
	if hSmall != 0 {
		sendMessageW.Call(hwnd, wmSetIcon, iconSmall, hSmall)
	}
}

func applyDarkTitle(hwnd uintptr) {
	dark := uint32(1)
	dwmSetAttr.Call(hwnd, dwmwaImmersiveDarkMode, uintptr(unsafe.Pointer(&dark)), 4)
	// Caption (title bar background): почти чёрный — сливается с UI
	capColor := colorref(0x0c, 0x0c, 0x12)
	dwmSetAttr.Call(hwnd, dwmwaCaptionColor, uintptr(unsafe.Pointer(&capColor)), 4)
	// Title text: акцентный фиолетовый
	textColor := colorref(0x7c, 0x6c, 0xff)
	dwmSetAttr.Call(hwnd, dwmwaTextColor, uintptr(unsafe.Pointer(&textColor)), 4)
	// Border: тёмная акцентная рамка
	borderColor := colorref(0x2a, 0x2a, 0x40)
	dwmSetAttr.Call(hwnd, dwmwaBorderColor, uintptr(unsafe.Pointer(&borderColor)), 4)
	// Скруглённые углы (Windows 11)
	cornerPref := uint32(2) // DWMWCP_ROUND
	dwmSetAttr.Call(hwnd, dwmwaWindowCornerPref, uintptr(unsafe.Pointer(&cornerPref)), 4)
	// Пробуем Mica backdrop (Windows 11 22H2+) — при неудаче молча игнорируется
	backdrop := uint32(backdropMica)
	dwmSetAttr.Call(hwnd, dwmwaSystemBackdropType, uintptr(unsafe.Pointer(&backdrop)), 4)
}

func applyLightTitle(hwnd uintptr) {
	dark := uint32(0)
	dwmSetAttr.Call(hwnd, dwmwaImmersiveDarkMode, uintptr(unsafe.Pointer(&dark)), 4)
	// Caption: светлый фон — сливается с UI
	capColor := colorref(0xf0, 0xf2, 0xf8)
	dwmSetAttr.Call(hwnd, dwmwaCaptionColor, uintptr(unsafe.Pointer(&capColor)), 4)
	// Title text: тёмный акцентный
	textColor := colorref(0x5b, 0x4d, 0xcc)
	dwmSetAttr.Call(hwnd, dwmwaTextColor, uintptr(unsafe.Pointer(&textColor)), 4)
	// Border: светлая рамка
	borderColor := colorref(0xc8, 0xcc, 0xe0)
	dwmSetAttr.Call(hwnd, dwmwaBorderColor, uintptr(unsafe.Pointer(&borderColor)), 4)
	// Скруглённые углы (Windows 11)
	cornerPref := uint32(2) // DWMWCP_ROUND
	dwmSetAttr.Call(hwnd, dwmwaWindowCornerPref, uintptr(unsafe.Pointer(&cornerPref)), 4)
	backdrop := uint32(backdropMica)
	dwmSetAttr.Call(hwnd, dwmwaSystemBackdropType, uintptr(unsafe.Pointer(&backdrop)), 4)
}

// applyThemeByName применяет стили titlebar по имени пресета.
func applyThemeByName(hwnd uintptr, name string) {
	switch name {
	case "light":
		applyLightTitle(hwnd)
	case "axiom":
		applyAxiomTitle(hwnd)
	case "hacker":
		applyHackerTitle(hwnd)
	case "midnight":
		applyMidnightTitle(hwnd)
	case "sepia":
		applySepiaTitle(hwnd)
	default:
		applyDarkTitle(hwnd)
	}
}

// applyAxiomTitle применяет DWM-цвета рамки под дизайн axiom-ui.
// CSS переменные:
//
//	--bg:        #070713  → фон рамки (почти чёрный с фиолетовым оттенком)
//	--on:        #2de89a  → title-text (зелёный акцент, виден только в панели задач)
//	--hairline2: ~#1e1e30 → бордер рамки
func applyAxiomTitle(hwnd uintptr) {
	dark := uint32(1)
	dwmSetAttr.Call(hwnd, dwmwaImmersiveDarkMode, uintptr(unsafe.Pointer(&dark)), 4)
	// Caption: --bg #070713
	capColor := colorref(0x07, 0x07, 0x13)
	dwmSetAttr.Call(hwnd, dwmwaCaptionColor, uintptr(unsafe.Pointer(&capColor)), 4)
	// Title text (виден только в Alt+Tab / панели задач): --on #2de89a
	textColor := colorref(0x2d, 0xe8, 0x9a)
	dwmSetAttr.Call(hwnd, dwmwaTextColor, uintptr(unsafe.Pointer(&textColor)), 4)
	// Border: --s1 #0c0c1d (темнее фона, соответствует hairline)
	borderColor := colorref(0x0c, 0x0c, 0x1d)
	dwmSetAttr.Call(hwnd, dwmwaBorderColor, uintptr(unsafe.Pointer(&borderColor)), 4)
	// Скруглённые углы (Windows 11) — совпадает с border-radius:30px в CSS
	cornerPref := uint32(2) // DWMWCP_ROUND
	dwmSetAttr.Call(hwnd, dwmwaWindowCornerPref, uintptr(unsafe.Pointer(&cornerPref)), 4)
	// Mica backdrop — прозрачность за окном, если поддерживается (Win 11 22H2+)
	backdrop := uint32(backdropMica)
	dwmSetAttr.Call(hwnd, dwmwaSystemBackdropType, uintptr(unsafe.Pointer(&backdrop)), 4)
}

// applyHackerTitle применяет DWM-цвета для «хакерской» темы:
//
//	--bg: #000000 (чёрный фон), --on: #00ff41 (зелёный текст)
func applyHackerTitle(hwnd uintptr) {
	dark := uint32(1)
	dwmSetAttr.Call(hwnd, dwmwaImmersiveDarkMode, uintptr(unsafe.Pointer(&dark)), 4)
	capColor := colorref(0x00, 0x00, 0x00)
	dwmSetAttr.Call(hwnd, dwmwaCaptionColor, uintptr(unsafe.Pointer(&capColor)), 4)
	textColor := colorref(0x00, 0xff, 0x41)
	dwmSetAttr.Call(hwnd, dwmwaTextColor, uintptr(unsafe.Pointer(&textColor)), 4)
	borderColor := colorref(0x0d, 0x22, 0x00)
	dwmSetAttr.Call(hwnd, dwmwaBorderColor, uintptr(unsafe.Pointer(&borderColor)), 4)
	cornerPref := uint32(2)
	dwmSetAttr.Call(hwnd, dwmwaWindowCornerPref, uintptr(unsafe.Pointer(&cornerPref)), 4)
	backdrop := uint32(backdropMica)
	dwmSetAttr.Call(hwnd, dwmwaSystemBackdropType, uintptr(unsafe.Pointer(&backdrop)), 4)
}

func applyMidnightTitle(hwnd uintptr) {
	dark := uint32(1)
	dwmSetAttr.Call(hwnd, dwmwaImmersiveDarkMode, uintptr(unsafe.Pointer(&dark)), 4)
	capColor := colorref(0x08, 0x08, 0x18)
	dwmSetAttr.Call(hwnd, dwmwaCaptionColor, uintptr(unsafe.Pointer(&capColor)), 4)
	textColor := colorref(0xa7, 0x8b, 0xfa)
	dwmSetAttr.Call(hwnd, dwmwaTextColor, uintptr(unsafe.Pointer(&textColor)), 4)
	borderColor := colorref(0x1a, 0x1a, 0x40)
	dwmSetAttr.Call(hwnd, dwmwaBorderColor, uintptr(unsafe.Pointer(&borderColor)), 4)
	cornerPref := uint32(2)
	dwmSetAttr.Call(hwnd, dwmwaWindowCornerPref, uintptr(unsafe.Pointer(&cornerPref)), 4)
	backdrop := uint32(backdropMica)
	dwmSetAttr.Call(hwnd, dwmwaSystemBackdropType, uintptr(unsafe.Pointer(&backdrop)), 4)
}

func applySepiaTitle(hwnd uintptr) {
	dark := uint32(1)
	dwmSetAttr.Call(hwnd, dwmwaImmersiveDarkMode, uintptr(unsafe.Pointer(&dark)), 4)
	capColor := colorref(0x1c, 0x15, 0x10)
	dwmSetAttr.Call(hwnd, dwmwaCaptionColor, uintptr(unsafe.Pointer(&capColor)), 4)
	textColor := colorref(0xc4, 0x93, 0x3f)
	dwmSetAttr.Call(hwnd, dwmwaTextColor, uintptr(unsafe.Pointer(&textColor)), 4)
	borderColor := colorref(0x3a, 0x2e, 0x1e)
	dwmSetAttr.Call(hwnd, dwmwaBorderColor, uintptr(unsafe.Pointer(&borderColor)), 4)
	cornerPref := uint32(2)
	dwmSetAttr.Call(hwnd, dwmwaWindowCornerPref, uintptr(unsafe.Pointer(&cornerPref)), 4)
	backdrop := uint32(backdropMica)
	dwmSetAttr.Call(hwnd, dwmwaSystemBackdropType, uintptr(unsafe.Pointer(&backdrop)), 4)
}

// Размеры окна под axiom-ui:
//
//	карточка 400×780, padding 20px с каждой стороны, тонкая рамка WS_THICKFRAME (~4px)
const (
	uiDefaultW = 448 // 400 + 20*2 + ~8 (рамка)
	uiDefaultH = 864 // 780 + 20*2 + ~44 (учёт DPI-rounded border + taskbar breathing room)
	uiMinW     = 440 // жёсткий минимум — не меньше карточки
	uiMinH     = 820 // жёсткий минимум по высоте
)

// windowState хранит позицию и размер окна.
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
	if s.Width < uiMinW || s.Height < uiMinH {
		return windowState{}, false
	}
	return s, true
}

// readRect читает текущие размеры/позицию окна через GetWindowRect.
// Возвращает false если hwnd невалиден или окно свёрнуто.
func readRect(hwnd uintptr) (windowState, bool) {
	// Проверяем что hwnd валиден
	ok, _, _ := isWindow.Call(hwnd)
	if ok == 0 {
		return windowState{}, false
	}
	var r [4]int32
	ret, _, _ := getWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r[0])))
	if ret == 0 {
		return windowState{}, false
	}
	w := r[2] - r[0]
	h := r[3] - r[1]
	if w < uiMinW || h < uiMinH {
		return windowState{}, false // свёрнуто или ошибка
	}
	return windowState{X: r[0], Y: r[1], Width: w, Height: h}, true
}

func writeState(s windowState) {
	data, _ := json.Marshal(s)
	// fileutil.WriteAtomic использует MoveFileExW MOVEFILE_REPLACE_EXISTING —
	// атомарная замена на NTFS. Уникальный tmp-файл устраняет гонку между
	// горутиной периодического сохранения и windowClose JS-биндингом.
	_ = fileutil.WriteAtomic(statePath, data, 0644)
}

func isZoomed(hwnd uintptr) bool {
	var wp [12]uint32
	wp[0] = uint32(unsafe.Sizeof(wp))
	getWindowPlacement.Call(hwnd, uintptr(unsafe.Pointer(&wp[0])))
	return wp[2] == showStateMaximized
}

// BringToFront переводит главное окно на передний план.
// Если окно свёрнуто — восстанавливает его. Если окно ещё не открыто — открывает.
func BringToFront(url string) {
	mu.Lock()
	win := instance
	isOpen := opened
	mu.Unlock()

	if !isOpen || win == nil {
		// Окно не открыто — открываем
		Open(url)
		return
	}

	// Окно открыто — находим HWND и переводим на передний план
	childHwnd := uintptr(unsafe.Pointer(win.Window()))
	rootHwnd, _, _ := getAncestor.Call(childHwnd, 2) // GA_ROOT
	if rootHwnd == 0 {
		rootHwnd = childHwnd
	}

	// Проверяем валидность окна
	ok, _, _ := isWindow.Call(rootHwnd)
	if ok == 0 {
		return
	}

	// Если свёрнуто — восстанавливаем
	minimized, _, _ := isIconic.Call(rootHwnd)
	if minimized != 0 {
		showWindowProc.Call(rootHwnd, swRestore)
	} else {
		showWindowProc.Call(rootHwnd, swShow)
	}

	// Переводим на передний план
	setForegroundWindowFn.Call(rootHwnd)
	bringWindowToTopFn.Call(rootHwnd)
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

		// Убираем нативный заголовок окна — HTML topbar становится строкой заголовка.
		// Сохраняем WS_THICKFRAME для изменения размера окна.
		const gwlStyleIdx = uintptr(^uintptr(0) - 15) // GWL_STYLE = -16 as uintptr on any arch
		style, _, _ := getWindowLong.Call(rootHwnd, gwlStyleIdx)
		setWindowLong.Call(rootHwnd, gwlStyleIdx, style&^wsCaption)
		// Перерасчёт рамки и принудительная переотрисовка клиентской области.
		var rect [4]int32
		getWindowRect.Call(rootHwnd, uintptr(unsafe.Pointer(&rect[0])))
		setWindowPos.Call(rootHwnd, 0,
			uintptr(rect[0]), uintptr(rect[1]),
			uintptr(rect[2]-rect[0]), uintptr(rect[3]-rect[1]),
			swpFrameChanged|swpNozorder|swpNoActivate)

		// Устанавливаем иконку из ресурсов .exe — панель задач + Alt+Tab.
		setAppIcon(rootHwnd)

		// Применяем тему axiom-ui — цвета рамки и DWM-бордера
		// совпадают с --bg:#070713 и --hairline2 из CSS.
		applyAxiomTitle(rootHwnd)

		// Заголовок используется только в панели задач.
		w.SetTitle("SafeSky")

		// Минимальный размер: карточка axiom-ui не должна обрезаться.
		w.SetSize(uiMinW, uiMinH, webview2.HintMin)

		// Применяем сохранённую позицию/размер.
		// SetWindowPos с SWP_NOZORDER|SWP_NOACTIVATE — без показа окна,
		// потому что WebView2 сам покажет окно при Navigate/Run.
		if s, ok := loadState(); ok {
			// Гарантируем что загруженный размер не меньше минимума
			if s.Width < uiMinW {
				s.Width = uiMinW
			}
			if s.Height < uiMinH {
				s.Height = uiMinH
			}
			setWindowPos.Call(rootHwnd, 0,
				uintptr(s.X), uintptr(s.Y),
				uintptr(s.Width), uintptr(s.Height),
				swpNozorder|swpNoActivate)
		} else {
			w.SetSize(uiDefaultW, uiDefaultH, webview2.HintNone)
		}

		// Горутина периодически сохраняет позицию пока окно живо.
		// Это гарантирует актуальное состояние независимо от способа закрытия:
		// кнопка X в заголовке, windowClose из JS, или Terminate().
		// Интервал 2с — минимальная нагрузка, достаточная точность.
		done := make(chan struct{})
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					if s, ok := readRect(rootHwnd); ok {
						writeState(s)
					}
				}
			}
		}()

		// windowClose из JS сохраняет состояние прямо перед закрытием
		// (на случай если periodic ещё не успел сохранить последнее положение).
		// setTitleTheme вызывается из JS при смене темы — мгновенно меняет цвет нативного titlebar.
		w.Bind("setTitleTheme", func(name string) {
			applyThemeByName(rootHwnd, name)
		})

		w.Bind("windowClose", func() {
			if s, ok := readRect(rootHwnd); ok {
				writeState(s)
			}
			postMessageW.Call(rootHwnd, wmClose, 0, 0)
		})

		w.Bind("windowDrag", func() {
			// Имитируем захват нативного заголовка окна — Windows обрабатывает перетаскивание.
			releaseCapture.Call()
			postMessageW.Call(rootHwnd, wmNcLButtonDown, htCaption, 0)
		})
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

		w.Navigate(url)
		w.Run()

		// Останавливаем горутину сохранения.
		close(done)

		// Финальное сохранение: пробуем прочитать позицию пока hwnd ещё может быть валиден.
		// Если hwnd уже уничтожен — readRect вернёт false и мы не затрём хорошее значение.
		if s, ok := readRect(rootHwnd); ok {
			writeState(s)
		}
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
