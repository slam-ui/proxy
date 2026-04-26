//go:build windows

package api

// handleProcIcon — GET /api/procicon?path=C:\path\to\app.exe
// Извлекает иконку .exe через Shell API, рендерит через GDI в RGBA-буфер
// и возвращает PNG. Результат кэшируется в памяти (до 256 записей, 30 мин TTL).

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ── Win32 API ────────────────────────────────────────────────────────────────

var (
	procShell32 = windows.NewLazySystemDLL("shell32.dll")
	procGdi32   = windows.NewLazySystemDLL("gdi32.dll")
	procUser32p = windows.NewLazySystemDLL("user32.dll")

	pSHGetFileInfoW     = procShell32.NewProc("SHGetFileInfoW")
	pCreateCompatibleDC = procGdi32.NewProc("CreateCompatibleDC")
	pCreateDIBSection   = procGdi32.NewProc("CreateDIBSection")
	pSelectObject       = procGdi32.NewProc("SelectObject")
	pDeleteDC           = procGdi32.NewProc("DeleteDC")
	pDeleteObject       = procGdi32.NewProc("DeleteObject")
	pDrawIconEx         = procUser32p.NewProc("DrawIconEx")
	pDestroyIcon        = procUser32p.NewProc("DestroyIcon")
)

const (
	shgfiIcon           = 0x0000_0100
	shgfiSmallIcon      = 0x0000_0001
	shgfiUseFileAttribs = 0x0000_0010
	diNormal            = 0x0003
	iconSize            = 32
)

type shFileInfo struct {
	HIcon         uintptr
	IIcon         int32
	DwAttributes  uint32
	SzDisplayName [260]uint16
	SzTypeName    [80]uint16
}

type bitmapInfoHeader struct {
	BiSize          uint32
	BiWidth         int32
	BiHeight        int32
	BiPlanes        uint16
	BiBitCount      uint16
	BiCompression   uint32
	BiSizeImage     uint32
	BiXPelsPerMeter int32
	BiYPelsPerMeter int32
	BiClrUsed       uint32
	BiClrImportant  uint32
}

type bitmapInfo struct {
	BmiHeader bitmapInfoHeader
	BmiColors [1]uint32
}

// ── Icon cache ────────────────────────────────────────────────────────────────

type iconCacheEntry struct {
	data      []byte
	createdAt time.Time
}

var (
	iconCacheMu  sync.Mutex
	iconCache    = map[string]*iconCacheEntry{}
	iconCacheTTL = 30 * time.Minute
	iconCacheMax = 256
)

func getIconCached(path string) []byte {
	iconCacheMu.Lock()
	defer iconCacheMu.Unlock()
	if e, ok := iconCache[path]; ok && time.Since(e.createdAt) < iconCacheTTL {
		return e.data
	}
	return nil
}

func setIconCached(path string, data []byte) {
	iconCacheMu.Lock()
	defer iconCacheMu.Unlock()
	// Простой eviction при переполнении — очищаем старые
	if len(iconCache) >= iconCacheMax {
		now := time.Now()
		for k, v := range iconCache {
			if now.Sub(v.createdAt) > iconCacheTTL {
				delete(iconCache, k)
			}
		}
		// Если всё ещё много — сбрасываем весь кэш
		if len(iconCache) >= iconCacheMax {
			iconCache = map[string]*iconCacheEntry{}
		}
	}
	iconCache[path] = &iconCacheEntry{data: data, createdAt: time.Now()}
}

// ── HTTP handler ──────────────────────────────────────────────────────────────

func (s *Server) handleProcIcon(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}
	// Принимаем только .exe файлы для безопасности
	if !strings.HasSuffix(strings.ToLower(path), ".exe") {
		http.Error(w, "only .exe supported", http.StatusBadRequest)
		return
	}

	// Проверяем кэш
	if cached := getIconCached(path); cached != nil {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=1800")
		w.Write(cached)
		return
	}

	// Извлекаем иконку
	data, err := extractExeIconPNG(path)
	if err != nil || len(data) == 0 {
		http.NotFound(w, r)
		return
	}

	setIconCached(path, data)
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=1800")
	w.Write(data)
}

// extractExeIconPNG извлекает маленькую иконку из .exe через SHGetFileInfoW,
// рендерит через GDI в 32×32 RGBA-буфер и возвращает PNG-байты.
func extractExeIconPNG(exePath string) ([]byte, error) {
	pathPtr, err := windows.UTF16PtrFromString(exePath)
	if err != nil {
		return nil, err
	}

	// 1. Получаем HICON через SHGetFileInfoW
	var fi shFileInfo
	flags := uintptr(shgfiIcon | shgfiSmallIcon | shgfiUseFileAttribs)
	ret, _, _ := pSHGetFileInfoW.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		0x80, // FILE_ATTRIBUTE_NORMAL
		uintptr(unsafe.Pointer(&fi)),
		uintptr(unsafe.Sizeof(fi)),
		flags,
	)
	if ret == 0 || fi.HIcon == 0 {
		return nil, nil
	}
	hIcon := fi.HIcon
	defer pDestroyIcon.Call(hIcon)

	// 2. Создаём совместимый DC (memory DC от desktop)
	hDC, _, _ := pCreateCompatibleDC.Call(0)
	if hDC == 0 {
		return nil, nil
	}
	defer pDeleteDC.Call(hDC)

	// 3. Создаём 32bpp DIB section для захвата пикселей
	bi := bitmapInfo{
		BmiHeader: bitmapInfoHeader{
			BiSize:     uint32(unsafe.Sizeof(bitmapInfoHeader{})),
			BiWidth:    iconSize,
			BiHeight:   -iconSize, // отрицательное = top-down
			BiPlanes:   1,
			BiBitCount: 32,
			// BI_RGB = 0, alpha в старшем байте каждого DWORD
		},
	}
	var pvBits unsafe.Pointer
	hBmp, _, _ := pCreateDIBSection.Call(
		hDC,
		uintptr(unsafe.Pointer(&bi)),
		0, // DIB_RGB_COLORS
		uintptr(unsafe.Pointer(&pvBits)),
		0, 0,
	)
	if hBmp == 0 {
		return nil, nil
	}
	defer pDeleteObject.Call(hBmp)

	// 4. Выбираем bitmap в DC
	pSelectObject.Call(hDC, hBmp)

	// 5. Рисуем иконку в DC
	pDrawIconEx.Call(
		hDC, 0, 0, hIcon,
		iconSize, iconSize,
		0, 0, diNormal,
	)

	// 6. Читаем пиксели — pvBits указывает на BGRA данные
	if pvBits == nil {
		return nil, nil
	}
	const stride = iconSize * 4
	pixelBytes := (*[iconSize * stride]byte)(pvBits)

	img := image.NewNRGBA(image.Rect(0, 0, iconSize, iconSize))
	for y := 0; y < iconSize; y++ {
		for x := 0; x < iconSize; x++ {
			base := (y*iconSize + x) * 4
			b := pixelBytes[base+0]
			g := pixelBytes[base+1]
			r := pixelBytes[base+2]
			a := pixelBytes[base+3]
			img.SetNRGBA(x, y, color.NRGBA{R: r, G: g, B: b, A: a})
		}
	}

	// 7. Кодируем в PNG
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
