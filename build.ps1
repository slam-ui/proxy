# build.ps1
param(
    [switch]$Release,
    [switch]$Clean,
    [switch]$NoGui,
    [string]$GoExePath = ""   # passed automatically when re-launching elevated
)

$ErrorActionPreference = "Stop"

# Always run from the directory where build.ps1 lives.
# When re-launched elevated, PowerShell starts in C:\Windows\system32
# instead of the project root -- go build fails with "go.mod not found".
Set-Location $PSScriptRoot

$DIST_DIR  = "dist"
$BINARY    = "proxy-client.exe"
$MAIN_PATH = ".\cmd\proxy-client"

Write-Host ""
Write-Host "========================================" -ForegroundColor Cyan
Write-Host "  Proxy Client -- Build and Package"     -ForegroundColor Cyan
Write-Host "========================================" -ForegroundColor Cyan
Write-Host ""

# --- Locate go.exe BEFORE elevation ----------------------
# In non-elevated context PATH is full and go.exe is visible.
# We find it here and pass the exact path to the elevated re-launch,
# so the elevated process never has to search a stripped PATH.
if ($GoExePath -eq "") {
    try {
        $GoExePath = (Get-Command go -ErrorAction Stop).Source
    } catch {
        # Try common locations as last resort
        foreach ($c in @(
            "C:\Program Files\Go\bin\go.exe",
            "C:\Go\bin\go.exe",
            "$env:LOCALAPPDATA\Programs\Go\bin\go.exe"
        )) {
            if (Test-Path $c) { $GoExePath = $c; break }
        }
    }
}

if (-not $GoExePath -or -not (Test-Path $GoExePath)) {
    Write-Host "[ERROR] go.exe not found. Please install Go from https://go.dev/dl/" -ForegroundColor Red
    Read-Host "Press Enter to close" | Out-Null
    exit 1
}

# --- Self-elevate passing the discovered go path ---------
$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole(
        [Security.Principal.WindowsBuiltInRole]::Administrator
)
if (-not $isAdmin) {
    Write-Host "Requesting Administrator rights ..." -ForegroundColor Yellow
    $argList = @("-NoExit", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", $MyInvocation.MyCommand.Path,
    "-GoExePath", $GoExePath)
    if ($Release) { $argList += "-Release" }
    if ($Clean)   { $argList += "-Clean"   }
    if ($NoGui)   { $argList += "-NoGui"   }
    Start-Process powershell -Verb RunAs -ArgumentList $argList
    exit 0
}

# --- Clean -----------------------------------------------
if ($Clean -and (Test-Path $DIST_DIR)) {
    Write-Host "Cleaning $DIST_DIR ..." -ForegroundColor Yellow
    Remove-Item -Recurse -Force $DIST_DIR
    Write-Host "[OK] Cleaned" -ForegroundColor Green
    Write-Host ""
}

if (-not (Test-Path $DIST_DIR)) {
    New-Item -ItemType Directory -Force -Path $DIST_DIR | Out-Null
}

# --- Stop running instance -------------------------------
# Strategy:
#   1. POST /api/quit  -- graceful: app disables proxy, closes UI, stops sing-box
#   2. Wait up to 5s for process to exit on its own
#   3. Force-kill only if still alive (we are admin, so Stop-Process works)
$running = Get-Process -Name "proxy-client" -ErrorAction SilentlyContinue
if ($running) {
    Write-Host "Stopping running proxy-client.exe ..." -ForegroundColor Yellow

    # Step 1 -- graceful shutdown via API
    # /api/quit closes the UI window first, then stops sing-box, then exits.
    # This avoids WebView2 crash that happens when the process is killed abruptly.
    try {
        Invoke-RestMethod -Uri "http://localhost:8080/api/quit" -Method POST -TimeoutSec 3 | Out-Null
        Write-Host "  Waiting for graceful exit..." -ForegroundColor Gray
    } catch { }

    # Step 2 -- wait up to 5s for clean exit
    $waited = 0
    while ($waited -lt 5000) {
        $check = Get-Process -Name "proxy-client" -ErrorAction SilentlyContinue
        if (-not $check) { break }
        Start-Sleep -Milliseconds 300
        $waited += 300
    }

    # Step 3 -- force kill if still alive
    $still = Get-Process -Name "proxy-client" -ErrorAction SilentlyContinue
    if ($still) {
        Write-Host "  Force killing..." -ForegroundColor DarkYellow
        $still | Stop-Process -Force -ErrorAction SilentlyContinue
        Start-Sleep -Milliseconds 800
    }

    $check = Get-Process -Name "proxy-client" -ErrorAction SilentlyContinue
    if ($check) {
        Write-Host "[ERROR] Could not stop proxy-client.exe" -ForegroundColor Red
        Read-Host "Press Enter to close" | Out-Null
        exit 1
    }

    Write-Host "[OK] Process stopped" -ForegroundColor Green
    Write-Host ""
}


# --- Run tests -------------------------------------------
Write-Host "Running tests ..." -ForegroundColor Yellow
$testResult = & $GoExePath test ./... -timeout 60s 2>&1
$testResult | ForEach-Object { Write-Host "  $_" -ForegroundColor DarkGray }
if ($LASTEXITCODE -ne 0) {
    Write-Host ""
    Write-Host "[ERROR] Tests failed — build aborted" -ForegroundColor Red
    Read-Host "Press Enter to close" | Out-Null
    exit 1
}
Write-Host "[OK] All tests passed" -ForegroundColor Green
Write-Host ""

# --- Patch window.go (native title bar + position persistence) ---------------
Write-Host "Patching internal\window\window.go ..." -ForegroundColor Yellow
$windowGoPath = "internal\window\window.go"
$windowGoContent = @'
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
'@
[System.IO.File]::WriteAllText(
        (Join-Path $PSScriptRoot $windowGoPath),
        $windowGoContent,
        [System.Text.Encoding]::UTF8
)
Write-Host "[OK] window.go patched" -ForegroundColor Green
Write-Host ""

# --- Compile ---------------------------------------------
Write-Host "Compiling $BINARY ..." -ForegroundColor Yellow
Write-Host "  go: $GoExePath" -ForegroundColor DarkGray

$ldflags = ""
if ($Release) {
    $ldflags = "-s -w"
    if (-not $NoGui) { $ldflags += " -H windowsgui" }
    Write-Host "  Mode: Release (stripped, no console)" -ForegroundColor Gray
} else {
    if (-not $NoGui) { $ldflags = "-H windowsgui" }
    Write-Host "  Mode: Debug" -ForegroundColor Gray
}

$env:GOOS        = "windows"
$env:GOARCH      = "amd64"
$env:CGO_ENABLED = "0"   # go-webview2/systray не требуют CGO (используют go-winloader)

$buildArgs = @("build", "-o", "$DIST_DIR\$BINARY")
if ($ldflags -ne "") {
    $buildArgs += @("-ldflags", $ldflags)
}
$buildArgs += $MAIN_PATH

& $GoExePath @buildArgs

if ($LASTEXITCODE -ne 0) {
    Write-Host "[ERROR] Compilation failed" -ForegroundColor Red
    Read-Host "Press Enter to close" | Out-Null
    exit 1
}
Write-Host "[OK] $BINARY compiled" -ForegroundColor Green

# --- Copy runtime files ----------------------------------
Write-Host ""
Write-Host "Copying runtime files ..." -ForegroundColor Yellow

$geoBins = Get-ChildItem -Path "." -Filter "geosite-*.bin"
foreach ($f in $geoBins) {
    Copy-Item $f.FullName "$DIST_DIR\$($f.Name)" -Force
    Write-Host "  [+] $($f.Name)" -ForegroundColor Gray
}

Copy-Item "routing.json" "$DIST_DIR\routing.json" -Force
Write-Host "  [+] routing.json" -ForegroundColor Gray
Write-Host "[OK] Runtime files copied" -ForegroundColor Green

# --- Placeholders ----------------------------------------
Write-Host ""
Write-Host "Checking placeholders ..." -ForegroundColor Yellow

$singBoxDest = "$DIST_DIR\sing-box.exe"
if (-not (Test-Path $singBoxDest)) {
    New-Item -ItemType File -Path $singBoxDest | Out-Null
    Write-Host "  [!] sing-box.exe  -- PLACEHOLDER, replace with real binary" -ForegroundColor DarkYellow
} else {
    Write-Host "  [=] sing-box.exe  -- already exists, skipped" -ForegroundColor Gray
}

$secretDest = "$DIST_DIR\secret.key"
if (-not (Test-Path $secretDest)) {
    $placeholder = "# Paste your VLESS link here, example:`n# vless://UUID@HOST:PORT?sni=SNI&pbk=PUBKEY&sid=SHORTID&flow=xtls-rprx-vision`n"
    Set-Content -Path $secretDest -Value $placeholder -Encoding UTF8
    Write-Host "  [!] secret.key    -- PLACEHOLDER, paste your VLESS link" -ForegroundColor DarkYellow
} else {
    Write-Host "  [=] secret.key    -- already exists, skipped" -ForegroundColor Gray
}

Write-Host "[OK] Placeholders ready" -ForegroundColor Green

# --- Summary ---------------------------------------------
Write-Host ""
Write-Host "========================================" -ForegroundColor Cyan
Write-Host "  dist/ contents:"                        -ForegroundColor Cyan
Write-Host "========================================" -ForegroundColor Cyan
Get-ChildItem $DIST_DIR | Sort-Object Name | ForEach-Object {
    $size = if ($_.Length -ge 1MB)     { "$([math]::Round($_.Length/1MB,1)) MB" }
    elseif ($_.Length -ge 1KB) { "$([math]::Round($_.Length/1KB,1)) KB" }
    else                       { "$($_.Length) B" }
    Write-Host ("  {0,-36} {1,8}" -f $_.Name, $size)
}
Write-Host ""

$exeMB = [math]::Round((Get-Item "$DIST_DIR\$BINARY").Length / 1MB, 2)
Write-Host "[OK] Build complete -- $BINARY = $exeMB MB" -ForegroundColor Green
Write-Host ""
Write-Host "Run:  .\$DIST_DIR\$BINARY" -ForegroundColor Cyan
Write-Host ""
Read-Host "Press Enter to close" | Out-Null