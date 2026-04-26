// tray_win32.go — кастомная Win32-реализация системного трея SafeSky.
//
// Owner-drawn меню с тёмной темой:
//   - Полный контроль над рендерингом (MF_OWNERDRAW + WM_DRAWITEM)
//   - Кастомные цвета, шрифт Segoe UI, акценты
//   - Левый клик по иконке → показ меню
//   - Правый клик по иконке → вызов OnOpen (переводит окно на передний план)
package tray

import (
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ── Win32 API ────────────────────────────────────────────────────────────────

var (
	user32dll   = windows.NewLazySystemDLL("user32.dll")
	shell32dll  = windows.NewLazySystemDLL("shell32.dll")
	uxthemedll  = windows.NewLazySystemDLL("uxtheme.dll")
	kernel32dll = windows.NewLazySystemDLL("kernel32.dll")
	gdi32dll    = windows.NewLazySystemDLL("gdi32.dll")

	pGetModuleHandle = kernel32dll.NewProc("GetModuleHandleW")

	pRegisterClassEx     = user32dll.NewProc("RegisterClassExW")
	pCreateWindowEx      = user32dll.NewProc("CreateWindowExW")
	pDefWindowProc       = user32dll.NewProc("DefWindowProcW")
	pGetMessage          = user32dll.NewProc("GetMessageW")
	pTranslateMessage    = user32dll.NewProc("TranslateMessage")
	pDispatchMessage     = user32dll.NewProc("DispatchMessageW")
	pPostQuitMessage     = user32dll.NewProc("PostQuitMessage")
	pDestroyWindow       = user32dll.NewProc("DestroyWindow")
	pCreatePopupMenu     = user32dll.NewProc("CreatePopupMenu")
	pAppendMenuW         = user32dll.NewProc("AppendMenuW")
	pTrackPopupMenu      = user32dll.NewProc("TrackPopupMenu")
	pDestroyMenu         = user32dll.NewProc("DestroyMenu")
	pSetForegroundWindow = user32dll.NewProc("SetForegroundWindow")
	pGetCursorPos        = user32dll.NewProc("GetCursorPos")
	pPostMessageW        = user32dll.NewProc("PostMessageW")
	pSetMenuInfo         = user32dll.NewProc("SetMenuInfo")
	pCreateIconFromRes   = user32dll.NewProc("CreateIconFromResource")
	pCreateIconFromResEx = user32dll.NewProc("CreateIconFromResourceEx")
	pLoadImage           = user32dll.NewProc("LoadImageW")
	pFillRect            = user32dll.NewProc("FillRect")
	pDrawTextW           = user32dll.NewProc("DrawTextW")

	pShellNotifyIcon = shell32dll.NewProc("Shell_NotifyIconW")

	pSetWindowTheme         = uxthemedll.NewProc("SetWindowTheme")
	pAllowDarkModeForWindow = uxthemedll.NewProc("AllowDarkModeForWindow")

	pRegisterWindowMessage = user32dll.NewProc("RegisterWindowMessageW")

	// GDI32
	pCreateSolidBrush = gdi32dll.NewProc("CreateSolidBrush")
	pCreateFontW      = gdi32dll.NewProc("CreateFontIndirectW")
	pCreateFontDirect = gdi32dll.NewProc("CreateFontW")
	pSelectObject     = gdi32dll.NewProc("SelectObject")
	pDeleteObject     = gdi32dll.NewProc("DeleteObject")
	pSetBkMode        = gdi32dll.NewProc("SetBkMode")
	pSetTextColor     = gdi32dll.NewProc("SetTextColor")
)

// wmTaskbarCreated — идентификатор зарегистрированного сообщения "TaskbarCreated".
var wmTaskbarCreated uintptr

// ── Constants ──────────────────────────────────────────────────────────────

const (
	wmUser       = 0x0400
	wmTrayIcon   = wmUser + 1
	wmShowMenu   = wmUser + 2
	wmBringFront = wmUser + 3
	wmQuitLoop   = wmUser + 4

	nimAdd        = 0
	nimModify     = 1
	nimDelete     = 2
	nimSetVersion = 4

	notifyIconVersion4 = 4

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004

	wmLButtonUp    = 0x0202
	wmRButtonUp    = 0x0205
	wmLButtonDblCk = 0x0203

	// NOTIFYICON_VERSION_4: Shell отправляет NIN_SELECT вместо WM_LBUTTONUP
	// при одинарном клике, и WM_CONTEXTMENU вместо WM_RBUTTONUP при правом клике.
	// Без обработки этих сообщений меню не открывается по клику на иконку.
	ninSelect     = 0x0400 // NIN_SELECT = WM_USER
	wmContextMenu = 0x007B // WM_CONTEXTMENU

	// AppendMenu flags
	mfString    = 0x0000
	mfSeparator = 0x0800
	mfGrayed    = 0x0001
	mfEnabled   = 0x0000
	mfChecked   = 0x0008
	mfUnChecked = 0x0000
	mfPopup     = 0x0010
	mfOwnerDraw = 0x0100

	// TrackPopupMenu flags
	tpmLeftButton  = 0x0000
	tpmBottomAlign = 0x0020

	// SetMenuInfo
	mimBackground = 0x00000002

	// WM_COMMAND / WM_MEASUREITEM / WM_DRAWITEM
	wmCommand     = 0x0111
	wmMeasureItem = 0x002C
	wmDrawItem    = 0x002B

	// Owner-draw item state
	odisSelected = 0x0001
	odisGrayed   = 0x0002

	// SetBkMode
	transparent = 1

	// DrawText flags
	dtLeft       = 0x0000
	dtRight      = 0x0002
	dtVCenter    = 0x0004
	dtSingleLine = 0x0020
	dtNoPrefix   = 0x0800

	// Menu IDs
	idOpen     = 1001
	idCopyAddr = 1002
	idEnable   = 1003
	idDisable  = 1004
	idQuit     = 1005
	idSrvBase  = 2000

	// ── Color palette (COLORREF = 0x00BBGGRR) ──
	// Тёмная тема SafeSky
	clrBg            = 0x001e0d0d // #0d0d1e — глубокий тёмно-синий фон
	clrBgHover       = 0x00351a1a // #1a1a35 — подсветка при наведении
	clrBgAccentHover = 0x0050201a // #1a2050 — акцентная подсветка
	clrBgDangerHover = 0x00182a35 // #352a18 — подсветка "Выход"
	clrText          = 0x00e4d4d0 // #d0d4e4 — основной текст
	clrTextDim       = 0x00644a4a // #4a4a64 — неактивный текст
	clrAccent        = 0x00ff8c6c // #6c8cff — акцентный синий
	clrDanger        = 0x005555ff // #ff5555 — красный для "Выход"
	clrSep           = 0x00301a1a // #1a1a30 — разделитель
	clrCheckBar      = 0x00ff8c6c // #6c8cff — полоска активного элемента

	// Размеры меню
	menuItemHeight = 32
	menuSepHeight  = 12
	menuItemWidth  = 280
	menuPadLeft    = 16
	menuPadRight   = 16
	menuBarWidth   = 3
)

// ── Structures ─────────────────────────────────────────────────────────────

type point struct {
	X, Y int32
}

type rect struct {
	Left, Top, Right, Bottom int32
}

type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UTimeout         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         [16]byte
	HBalloonIcon     uintptr
}

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}

type menuInfo struct {
	CbSize          uint32
	FMask           uint32
	DwStyle         uint32
	CyMax           uint32
	HbrBack         uintptr
	DwContextHelpID uint32
	DwMenuData      uintptr
}

type measureItemStruct struct {
	CtlType    uint32
	CtlID      uint32
	ItemID     uint32
	ItemWidth  uint32
	ItemHeight uint32
	ItemData   uintptr
}

type drawItemStruct struct {
	CtlType    uint32
	CtlID      uint32
	ItemID     uint32
	ItemAction uint32
	ItemState  uint32
	HwndItem   uintptr
	HDC        uintptr
	RcItem     rect
	ItemData   uintptr
}

// ── Owner-drawn menu items ───────────────────────────────────────────────

type odItemKind int

const (
	odNormal odItemKind = iota
	odSep
	odAccent // «Открыть SafeSky» — жирный + акцентный цвет
	odDanger // «Выход» — красный при наведении
)

type odItem struct {
	kind    odItemKind
	text    string
	subtext string // текст справа (пинг, адрес)
	id      int
	enabled bool
	checked bool
	popup   bool // элемент с подменю (стрелка ▸)
}

var (
	// currentODItems — элементы текущего открытого меню.
	// Заполняется в showTrayMenu, используется в WM_MEASUREITEM / WM_DRAWITEM.
	// Безопасно: showTrayMenu блокируется на TrackPopupMenu, все WM_ приходят в этот же поток.
	currentODItems []odItem

	// Кэшированные шрифты (создаются один раз)
	menuFontNormal uintptr
	menuFontBold   uintptr

	// Кэшированная кисть фона меню
	menuBgBrush uintptr
)

// ── Backend state ─────────────────────────────────────────────────────────

var (
	win32mu         sync.Mutex
	win32hwnd       uintptr
	win32hicon      uintptr
	win32tooltipOn  [128]uint16
	win32tooltipOff [128]uint16

	win32Running = false

	win32MenuState struct {
		sync.Mutex
		open           bool
		enableEnabled  bool
		disableEnabled bool
		copyAddr       string
		servers        []ServerItem
	}

	win32BringFront func()
)

// ── Entry points (called by tray.go) ─────────────────────────────────────

func win32Run(onReady func(), onExit func()) {
	win32mu.Lock()
	if win32Running {
		win32mu.Unlock()
		return
	}
	win32Running = true
	win32mu.Unlock()

	taskbarCreatedStr, _ := windows.UTF16PtrFromString("TaskbarCreated")
	wmTaskbarCreated, _, _ = pRegisterWindowMessage.Call(uintptr(unsafe.Pointer(taskbarCreatedStr)))
	runtime.KeepAlive(taskbarCreatedStr)

	copyUTF16(&win32tooltipOn, "SafeSky — Включён")
	copyUTF16(&win32tooltipOff, "SafeSky — Выключен")

	className, _ := windows.UTF16PtrFromString("SafeSkyTrayWnd")
	hInstance, _, _ := pGetModuleHandle.Call(0)

	wcx := wndClassEx{
		CbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		LpfnWndProc:   syscall.NewCallback(trayWndProc),
		HInstance:     hInstance,
		LpszClassName: className,
	}
	pRegisterClassEx.Call(uintptr(unsafe.Pointer(&wcx)))

	hwnd, _, _ := pCreateWindowEx.Call(
		0, uintptr(unsafe.Pointer(className)), 0,
		0x80000000, // WS_POPUP
		0, 0, 0, 0,
		0, 0, hInstance, 0)
	if hwnd == 0 {
		hwnd, _, _ = pCreateWindowEx.Call(
			0, uintptr(unsafe.Pointer(className)), 0,
			0, 0, 0, 0, 0,
			0, 0, hInstance, 0)
	}
	win32hwnd = hwnd

	// Dark mode
	if pAllowDarkModeForWindow.Find() == nil {
		pAllowDarkModeForWindow.Call(hwnd, 1)
	}
	themeStr, _ := windows.UTF16PtrFromString("DarkMode_Explorer")
	pSetWindowTheme.Call(hwnd, uintptr(unsafe.Pointer(themeStr)), 0)

	initialIcon := buildIconHandle(iconOff())
	win32mu.Lock()
	win32hicon = initialIcon
	win32mu.Unlock()

	nid := buildNID(hwnd, initialIcon, win32tooltipOff)
	pShellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
	win32SetTrayVersion(hwnd)

	go onReady()

	var m msg
	for {
		r, _, _ := pGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if r == 0 || r == ^uintptr(0) {
			break
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}

	nid2 := buildNID(hwnd, 0, win32tooltipOff)
	pShellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&nid2)))
	pDestroyWindow.Call(hwnd)

	onExit()
}

func win32Quit() {
	if win32hwnd != 0 {
		pPostMessageW.Call(win32hwnd, wmQuitLoop, 0, 0)
	}
}

func win32SetIcon(enabled bool) {
	win32mu.Lock()
	var h uintptr
	if enabled {
		h = buildIconHandle(iconOn())
	} else {
		h = buildIconHandle(iconOff())
	}
	win32hicon = h
	win32mu.Unlock()

	if win32hwnd == 0 {
		return
	}
	nid := buildNID(win32hwnd, h, win32tooltipOff)
	if enabled {
		copyUTF16(&nid.SzTip, "SafeSky — Включён")
	}
	pShellNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
}

func win32SetTooltip(tip string) {
	if win32hwnd == 0 {
		return
	}
	nid := buildNID(win32hwnd, win32hicon, win32tooltipOff)
	copyUTF16(&nid.SzTip, tip)
	pShellNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
}

// ── Window procedure ──────────────────────────────────────────────────────

func trayWndProc(hwnd, uMsg, wParam, lParam uintptr) uintptr {
	// WM_TASKBARCREATED — восстановление иконки после перезапуска Explorer
	if wmTaskbarCreated != 0 && uMsg == wmTaskbarCreated {
		win32mu.Lock()
		h := win32hicon
		win32mu.Unlock()
		tip := win32tooltipOff
		win32MenuState.Lock()
		isEnabled := win32MenuState.disableEnabled
		win32MenuState.Unlock()
		if isEnabled {
			tip = win32tooltipOn
		}
		nid := buildNID(hwnd, h, tip)
		pShellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
		win32SetTrayVersion(hwnd)
		return 0
	}

	switch uMsg {
	case wmTrayIcon:
		mouseMsg := lParam & 0xFFFF
		switch mouseMsg {
		case wmLButtonUp, wmLButtonDblCk, ninSelect:
			// NIN_SELECT: NOTIFYICON_VERSION_4 отправляет его вместо WM_LBUTTONUP
			// при одинарном клике. Без этого меню не открывается на Windows 10/11.
			pPostMessageW.Call(hwnd, wmShowMenu, 0, 0)
		case wmRButtonUp, wmContextMenu:
			// WM_CONTEXTMENU: NOTIFYICON_VERSION_4 отправляет его вместо WM_RBUTTONUP.
			pPostMessageW.Call(hwnd, wmBringFront, 0, 0)
		}
		return 0

	case wmShowMenu:
		showTrayMenu(hwnd)
		return 0

	case wmBringFront:
		if win32BringFront != nil {
			go win32BringFront()
		}
		return 0

	case wmQuitLoop:
		pPostQuitMessage.Call(0)
		return 0

	case wmCommand:
		cmdID := int(wParam & 0xFFFF)
		go handleMenuCommand(cmdID)
		return 0

	case wmMeasureItem:
		handleMeasureItem(lParam)
		return 1

	case wmDrawItem:
		handleDrawItem(lParam)
		return 1
	}

	r, _, _ := pDefWindowProc.Call(hwnd, uMsg, wParam, lParam)
	return r
}

// ── Menu construction (owner-drawn) ──────────────────────────────────────

func showTrayMenu(hwnd uintptr) {
	win32MenuState.Lock()
	enableEnabled := win32MenuState.enableEnabled
	disableEnabled := win32MenuState.disableEnabled
	copyAddr := win32MenuState.copyAddr
	servers := make([]ServerItem, len(win32MenuState.servers))
	copy(servers, win32MenuState.servers)
	win32MenuState.Unlock()

	warmingMu.Lock()
	isWarming := warmingActive
	warmingMu.Unlock()

	// Сбрасываем список owner-draw элементов
	currentODItems = currentODItems[:0]

	hMenu, _, _ := pCreatePopupMenu.Call()
	if hMenu == 0 {
		return
	}
	defer pDestroyMenu.Call(hMenu)

	// Dark mode theme для рамки меню
	themeStr, _ := windows.UTF16PtrFromString("DarkMode_Explorer")
	pSetWindowTheme.Call(hwnd, uintptr(unsafe.Pointer(themeStr)), 0)

	// Фон меню
	setMenuBackground(hMenu)

	// ── Элементы меню ──

	addOD(hMenu, odItem{kind: odAccent, text: "SafeSky", id: idOpen, enabled: true})
	addODSep(hMenu)

	// Статус подключения
	if isWarming {
		addOD(hMenu, odItem{kind: odNormal, text: "Запуск...", id: 0, enabled: false})
	} else if disableEnabled {
		addOD(hMenu, odItem{kind: odNormal, text: "Подключено", id: 0, enabled: false})
	} else {
		addOD(hMenu, odItem{kind: odNormal, text: "Отключено", id: 0, enabled: false})
	}
	addODSep(hMenu)

	if copyAddr != "" {
		addOD(hMenu, odItem{kind: odNormal, text: "Копировать адрес", subtext: copyAddr, id: idCopyAddr, enabled: true})
	} else {
		addOD(hMenu, odItem{kind: odNormal, text: "Копировать адрес", id: idCopyAddr, enabled: false})
	}
	addODSep(hMenu)

	addOD(hMenu, odItem{kind: odNormal, text: "Включить прокси", id: idEnable, enabled: enableEnabled && !isWarming})
	addOD(hMenu, odItem{kind: odNormal, text: "Выключить прокси", id: idDisable, enabled: disableEnabled && !isWarming})

	// Подменю серверов
	if len(servers) > 0 {
		addODSep(hMenu)
		hSub, _, _ := pCreatePopupMenu.Call()
		if hSub != 0 {
			setMenuBackground(hSub)
			for i, srv := range servers {
				if i >= maxServerSlots {
					break
				}
				addOD(hSub, odItem{
					kind:    odNormal,
					text:    srv.Name,
					subtext: srv.Ping,
					id:      idSrvBase + i,
					enabled: true,
					checked: srv.Active,
				})
			}
			addODPopup(hMenu, hSub, odItem{kind: odNormal, text: "Серверы", enabled: true, popup: true})
		}
	}

	addODSep(hMenu)
	addOD(hMenu, odItem{kind: odDanger, text: "Выход", id: idQuit, enabled: true})

	// Показываем меню
	var pt point
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	pSetForegroundWindow.Call(hwnd)
	pTrackPopupMenu.Call(hMenu,
		tpmLeftButton|tpmBottomAlign,
		uintptr(pt.X), uintptr(pt.Y),
		0, hwnd, 0)
	pPostMessageW.Call(hwnd, 0, 0, 0)
}

func handleMenuCommand(id int) {
	switch {
	case id == idOpen:
		if cb.OnOpen != nil {
			cb.OnOpen()
		}
	case id == idCopyAddr:
		if cb.OnCopyAddr != nil {
			win32MenuState.Lock()
			addr := win32MenuState.copyAddr
			win32MenuState.Unlock()
			cb.OnCopyAddr(addr)
		}
	case id == idEnable:
		if cb.OnEnable != nil {
			cb.OnEnable()
		}
	case id == idDisable:
		if cb.OnDisable != nil {
			cb.OnDisable()
		}
	case id == idQuit:
		win32Quit()
		if cb.OnQuit != nil {
			cb.OnQuit()
		}
	case id >= idSrvBase && id < idSrvBase+maxServerSlots:
		idx := id - idSrvBase
		win32MenuState.Lock()
		var srvID string
		if idx < len(win32MenuState.servers) {
			srvID = win32MenuState.servers[idx].ID
		}
		win32MenuState.Unlock()
		if srvID != "" && cb.OnServerSwitch != nil {
			cb.OnServerSwitch(srvID)
		}
	}
}

// ── Owner-drawn: добавление элементов ────────────────────────────────────

func addOD(hMenu uintptr, item odItem) {
	idx := len(currentODItems)
	currentODItems = append(currentODItems, item)
	flags := uintptr(mfOwnerDraw)
	if !item.enabled {
		flags |= mfGrayed
	}
	pAppendMenuW.Call(hMenu, flags, uintptr(item.id), uintptr(idx))
}

func addODSep(hMenu uintptr) {
	idx := len(currentODItems)
	currentODItems = append(currentODItems, odItem{kind: odSep})
	pAppendMenuW.Call(hMenu, uintptr(mfOwnerDraw), 0, uintptr(idx))
}

func addODPopup(hMenu, hSub uintptr, item odItem) {
	idx := len(currentODItems)
	currentODItems = append(currentODItems, item)
	flags := uintptr(mfOwnerDraw | mfPopup)
	pAppendMenuW.Call(hMenu, flags, hSub, uintptr(idx))
}

// ── Owner-drawn: WM_MEASUREITEM ──────────────────────────────────────────

func handleMeasureItem(lParam uintptr) {
	mi := win32MessageStruct[measureItemStruct](lParam)
	// ODT_MENU = 1 (not 4 which is ODT_BUTTON)
	if mi.CtlType != 1 {
		return
	}
	idx := int(mi.ItemData)
	if idx < 0 || idx >= len(currentODItems) {
		mi.ItemWidth = uint32(menuItemWidth)
		mi.ItemHeight = uint32(menuItemHeight)
		return
	}
	item := &currentODItems[idx]
	if item.kind == odSep {
		mi.ItemWidth = uint32(menuItemWidth)
		mi.ItemHeight = uint32(menuSepHeight)
		return
	}
	mi.ItemWidth = uint32(menuItemWidth)
	mi.ItemHeight = uint32(menuItemHeight)
}

// ── Owner-drawn: WM_DRAWITEM ─────────────────────────────────────────────

func handleDrawItem(lParam uintptr) {
	di := win32MessageStruct[drawItemStruct](lParam)
	if di.CtlType != 1 { // ODT_MENU = 1
		return
	}
	idx := int(di.ItemData)
	if idx < 0 || idx >= len(currentODItems) {
		return
	}
	item := &currentODItems[idx]
	hdc := di.HDC
	rc := di.RcItem
	selected := di.ItemState&odisSelected != 0

	// ── Фон ──
	bgColor := uint32(clrBg)
	if selected && item.enabled && item.kind != odSep {
		switch item.kind {
		case odAccent:
			bgColor = uint32(clrBgAccentHover)
		case odDanger:
			bgColor = uint32(clrBgDangerHover)
		default:
			bgColor = uint32(clrBgHover)
		}
	}
	fillRectColor(hdc, &rc, bgColor)

	// ── Разделитель ──
	if item.kind == odSep {
		sepY := (rc.Top + rc.Bottom) / 2
		sepRC := rect{
			Left:   rc.Left + int32(menuPadLeft),
			Top:    sepY,
			Right:  rc.Right - int32(menuPadRight),
			Bottom: sepY + 1,
		}
		fillRectColor(hdc, &sepRC, uint32(clrSep))
		return
	}

	// ── Полоска-индикатор для checked элементов ──
	if item.checked {
		barRC := rect{
			Left:   rc.Left,
			Top:    rc.Top + 6,
			Right:  rc.Left + int32(menuBarWidth),
			Bottom: rc.Bottom - 6,
		}
		fillRectColor(hdc, &barRC, uint32(clrCheckBar))
	}

	// ── Текст ──
	pSetBkMode.Call(hdc, transparent)

	// Выбираем цвет текста
	textColor := uint32(clrText)
	if !item.enabled {
		textColor = uint32(clrTextDim)
	} else {
		switch item.kind {
		case odAccent:
			textColor = uint32(clrAccent)
		case odDanger:
			if selected {
				textColor = uint32(clrDanger)
			}
		}
	}
	pSetTextColor.Call(hdc, uintptr(textColor))

	// Выбираем шрифт
	font := getMenuFont()
	if item.kind == odAccent {
		font = getMenuFontBold()
	}
	oldFont, _, _ := pSelectObject.Call(hdc, font)

	// Область текста с отступами
	padLeft := int32(menuPadLeft)
	if item.checked {
		padLeft = int32(menuPadLeft) + int32(menuBarWidth) + 4 // чуть правее полоски
	}
	textRC := rect{
		Left:   rc.Left + padLeft,
		Top:    rc.Top,
		Right:  rc.Right - int32(menuPadRight),
		Bottom: rc.Bottom,
	}

	// Основной текст (слева)
	drawTextStr(hdc, item.text, &textRC, dtLeft|dtVCenter|dtSingleLine|dtNoPrefix)

	// Вторичный текст справа (пинг, адрес) или стрелка подменю
	if item.popup {
		// Стрелка подменю
		arrowRC := rect{
			Left:   rc.Right - 28,
			Top:    rc.Top,
			Right:  rc.Right - 8,
			Bottom: rc.Bottom,
		}
		dimColor := uint32(clrTextDim)
		if selected && item.enabled {
			dimColor = uint32(clrText)
		}
		pSetTextColor.Call(hdc, uintptr(dimColor))
		drawTextStr(hdc, "\u25B8", &arrowRC, dtRight|dtVCenter|dtSingleLine|dtNoPrefix)
	} else if item.subtext != "" {
		subtextColor := uint32(clrTextDim)
		if selected && item.enabled {
			subtextColor = uint32(clrText)
		}
		pSetTextColor.Call(hdc, uintptr(subtextColor))
		drawTextStr(hdc, item.subtext, &textRC, dtRight|dtVCenter|dtSingleLine|dtNoPrefix)
	}

	// Восстанавливаем шрифт
	pSelectObject.Call(hdc, oldFont)
}

func win32MessageStruct[T any](addr uintptr) *T {
	// lParam contains an OS-owned pointer for WM_MEASUREITEM/WM_DRAWITEM.
	return (*T)(unsafe.Add(unsafe.Pointer(nil), addr))
}

// ── Font helpers ─────────────────────────────────────────────────────────

func getMenuFont() uintptr {
	if menuFontNormal != 0 {
		return menuFontNormal
	}
	menuFontNormal = createMenuFont(400) // FW_NORMAL
	return menuFontNormal
}

func getMenuFontBold() uintptr {
	if menuFontBold != 0 {
		return menuFontBold
	}
	menuFontBold = createMenuFont(600) // FW_SEMIBOLD
	return menuFontBold
}

func createMenuFont(weight int) uintptr {
	name, _ := windows.UTF16PtrFromString("Segoe UI")
	// nHeight = -15 ≈ 10pt при 96 DPI. Negative = character height.
	// Go 1.24+: отрицательные константы не конвертируются в uintptr напрямую.
	// Используем промежуточную переменную int32 → uintptr для корректного two's complement.
	var nHeight int32 = -15
	h, _, _ := pCreateFontDirect.Call(
		uintptr(nHeight), // nHeight
		0,                // nWidth
		0,                // nEscapement
		0,                // nOrientation
		uintptr(weight),  // fnWeight
		0,                // fdwItalic
		0,                // fdwUnderline
		0,                // fdwStrikeOut
		1,                // fdwCharSet = DEFAULT_CHARSET
		0,                // fdwOutputPrecision
		0,                // fdwClipPrecision
		5,                // fdwQuality = CLEARTYPE_QUALITY
		0,                // fdwPitchAndFamily
		uintptr(unsafe.Pointer(name)),
	)
	runtime.KeepAlive(name)
	return h
}

// ── GDI helpers ──────────────────────────────────────────────────────────

func fillRectColor(hdc uintptr, rc *rect, colorref uint32) {
	brush, _, _ := pCreateSolidBrush.Call(uintptr(colorref))
	if brush != 0 {
		pFillRect.Call(hdc, uintptr(unsafe.Pointer(rc)), brush)
		pDeleteObject.Call(brush)
	}
}

func drawTextStr(hdc uintptr, text string, rc *rect, flags uint32) {
	ptr, _ := windows.UTF16PtrFromString(text)
	// Go 1.24+: отрицательные константы не конвертируются в uintptr напрямую.
	var nCount int32 = -1 // null-terminated string
	pDrawTextW.Call(
		hdc,
		uintptr(unsafe.Pointer(ptr)),
		uintptr(nCount),
		uintptr(unsafe.Pointer(rc)),
		uintptr(flags),
	)
	runtime.KeepAlive(ptr)
}

func setMenuBackground(hMenu uintptr) {
	if menuBgBrush == 0 {
		menuBgBrush, _, _ = pCreateSolidBrush.Call(uintptr(uint32(clrBg)))
	}
	mi := menuInfo{
		CbSize:  uint32(unsafe.Sizeof(menuInfo{})),
		FMask:   mimBackground,
		HbrBack: menuBgBrush,
	}
	pSetMenuInfo.Call(hMenu, uintptr(unsafe.Pointer(&mi)))
}

func createSolidBrush(colorref uint32) uintptr {
	h, _, _ := pCreateSolidBrush.Call(uintptr(colorref))
	return h
}

// ── Icon helpers ──────────────────────────────────────────────────────────

func buildIconHandle(data []byte) uintptr {
	const (
		imageIcon     = 1
		lrShared      = 0x8000
		lrDefaultSize = 0x0040
	)
	hMod, _, _ := pGetModuleHandle.Call(0)
	if hMod != 0 {
		h, _, _ := pLoadImage.Call(hMod, 1, imageIcon, 16, 16, lrShared)
		if h != 0 {
			return h
		}
		h, _, _ = pLoadImage.Call(hMod, 1, imageIcon, 0, 0, lrShared|lrDefaultSize)
		if h != 0 {
			return h
		}
	}

	if len(data) < 22 {
		return 0
	}
	offset := uint32(data[18]) | uint32(data[19])<<8 | uint32(data[20])<<16 | uint32(data[21])<<24
	size := uint32(data[14]) | uint32(data[15])<<8 | uint32(data[16])<<16 | uint32(data[17])<<24
	if size == 0 || uint32(len(data)) < offset+size {
		return 0
	}
	imgData := data[offset : offset+size]

	const lrDefaultColor = 0x0000
	h, _, _ := pCreateIconFromResEx.Call(
		uintptr(unsafe.Pointer(&imgData[0])),
		uintptr(size),
		1,
		0x00030000,
		16, 16,
		lrDefaultColor,
	)
	if h != 0 {
		runtime.KeepAlive(imgData)
		return h
	}

	h, _, _ = pCreateIconFromRes.Call(
		uintptr(unsafe.Pointer(&imgData[0])),
		uintptr(size),
		1,
		0x00030000,
	)
	runtime.KeepAlive(imgData)
	return h
}

// ── NID helpers ──────────────────────────────────────────────────────────

func win32SetTrayVersion(hwnd uintptr) {
	nid := notifyIconData{
		HWnd:     hwnd,
		UID:      1,
		UTimeout: notifyIconVersion4,
	}
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	pShellNotifyIcon.Call(nimSetVersion, uintptr(unsafe.Pointer(&nid)))
}

func buildNID(hwnd, hicon uintptr, tip [128]uint16) notifyIconData {
	nid := notifyIconData{
		HWnd:             hwnd,
		UID:              1,
		UFlags:           nifMessage | nifIcon | nifTip,
		UCallbackMessage: wmTrayIcon,
		HIcon:            hicon,
		SzTip:            tip,
	}
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	return nid
}

func copyUTF16(dst *[128]uint16, s string) {
	src, _ := windows.UTF16FromString(s)
	n := len(src)
	if n > 127 {
		n = 127
	}
	for i := 0; i < n; i++ {
		dst[i] = src[i]
	}
	dst[n] = 0
}
