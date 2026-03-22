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
	user32             = windows.NewLazyDLL("user32.dll")
	dwmAPI             = windows.NewLazyDLL("dwmapi.dll")
	setWindowPos       = user32.NewProc("SetWindowPos")
	getWindowRect      = user32.NewProc("GetWindowRect")
	postMessageW       = user32.NewProc("PostMessageW")
	getWindowPlacement = user32.NewProc("GetWindowPlacement")
	getAncestor        = user32.NewProc("GetAncestor")
	isWindow           = user32.NewProc("IsWindow")
	dwmSetAttr         = dwmAPI.NewProc("DwmSetWindowAttribute")
)

const (
	swpNozorder        = 0x0004
	swpNoActivate      = 0x0010
	swpNoSize          = 0x0001
	wmSysCommand       = 0x0112
	wmClose            = 0x0010
	scMinimize         = 0xF020
	scMaximize         = 0xF030
	scRestore          = 0xF120
	showStateMaximized = 3

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
	if s.Width < 400 || s.Height < 300 {
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
	if w < 400 || h < 300 {
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

		applyDarkTitle(rootHwnd)

		// Устанавливаем заголовок окна
		w.SetTitle("Proxy Client")

		// Применяем сохранённую позицию/размер.
		// SetWindowPos с SWP_NOZORDER|SWP_NOACTIVATE — без показа окна,
		// потому что WebView2 сам покажет окно при Navigate/Run.
		if s, ok := loadState(); ok {
			setWindowPos.Call(rootHwnd, 0,
				uintptr(s.X), uintptr(s.Y),
				uintptr(s.Width), uintptr(s.Height),
				swpNozorder|swpNoActivate)
		} else {
			w.SetSize(960, 640, webview2.HintNone)
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
