#Requires -Version 5.1
<#
.SYNOPSIS
    Сборка и упаковка proxy-client в папку dist/
.PARAMETER Release
    Компилировать без отладочной информации и без консольного окна
.PARAMETER Clean
    Полностью удалить dist/ перед сборкой
.PARAMETER NoGui
    Оставить консольное окно (удобно для отладки)
.PARAMETER SkipTests
    Пропустить тесты
.PARAMETER GoExePath
    Явный путь к go.exe
.EXAMPLE
    .\build.ps1                  # Debug
    .\build.ps1 -Release         # Release для раздачи
    .\build.ps1 -Release -Clean  # Чистая Release-сборка
#>
param(
    [switch]$Release,
    [switch]$Clean,
    [switch]$NoGui,
    [switch]$SkipTests,
    [string]$GoExePath = ""
)

$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

$DIST_DIR  = "dist"
$BINARY    = "proxy-client.exe"
$MAIN_PATH = ".\cmd\proxy-client"
$DATA_DIR  = "$DIST_DIR\data"

function Write-Banner([string]$text) {
    $line = "-" * ($text.Length + 4)
    Write-Host ""; Write-Host "  +$line+" -ForegroundColor Cyan
    Write-Host "  |  $text  |" -ForegroundColor Cyan
    Write-Host "  +$line+" -ForegroundColor Cyan; Write-Host ""
}
function Write-Step([string]$text) { Write-Host ">> $text" -ForegroundColor Yellow }
function Write-OK([string]$text)   { Write-Host "  OK  $text" -ForegroundColor Green }
function Write-Skip([string]$text) { Write-Host "  ..  $text" -ForegroundColor DarkGray }
function Write-Warn([string]$text) { Write-Host "  !!  $text" -ForegroundColor DarkYellow }
function Write-Fail([string]$text) { Write-Host ""; Write-Host "  XX  $text" -ForegroundColor Red; Write-Host "" }

Write-Banner "proxy-client build"

# Admin check
$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole(
        [Security.Principal.WindowsBuiltInRole]::Administrator)

if (-not $isAdmin) {
    Write-Step "Requesting Administrator rights..."

    # Используем $PSCommandPath (более надежно) и фильтруем пустые значения
    $argList = @("-NoExit", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "`"$PSCommandPath`"")

    # Добавляем параметры только если они переданы
    if ($GoExePath) { $argList += @("-GoExePath", "`"$GoExePath`"") }
    if ($Release)   { $argList += "-Release"   }
    if ($Clean)     { $argList += "-Clean"     }
    if ($NoGui)     { $argList += "-NoGui"     }
    if ($SkipTests) { $argList += "-SkipTests" }

    Start-Process powershell -Verb RunAs -ArgumentList $argList
    exit 0
}

# Find Go
Write-Step "Locating Go toolchain..."
if ($GoExePath -eq "") {
    try { $GoExePath = (Get-Command go -ErrorAction Stop).Source } catch {}
    if (-not $GoExePath) {
        foreach ($c in @("C:\Program Files\Go\bin\go.exe","C:\Go\bin\go.exe","$env:LOCALAPPDATA\Programs\Go\bin\go.exe")) {
            if (Test-Path $c) { $GoExePath = $c; break }
        }
    }
}
if (-not $GoExePath -or -not (Test-Path $GoExePath)) {
    Write-Fail "go.exe not found. Install from https://go.dev/dl/"; Read-Host "Press Enter"; exit 1
}
Write-OK "$(& $GoExePath version)"

# Stop running instance
$running = Get-Process -Name "proxy-client" -ErrorAction SilentlyContinue
if ($running) {
    Write-Step "Stopping running proxy-client.exe..."
    try { Invoke-RestMethod -Uri "http://localhost:8080/api/quit" -Method POST -TimeoutSec 3 | Out-Null } catch {}
    $waited = 0
    while ($waited -lt 5000) {
        if (-not (Get-Process -Name "proxy-client" -ErrorAction SilentlyContinue)) { break }
        Start-Sleep -Milliseconds 300; $waited += 300
    }
    if (Get-Process -Name "proxy-client" -ErrorAction SilentlyContinue) {
        Get-Process -Name "proxy-client" -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
        Start-Sleep -Milliseconds 800
    }
    if (Get-Process -Name "proxy-client" -ErrorAction SilentlyContinue) {
        Write-Fail "Could not stop proxy-client.exe -- close it manually and retry"; Read-Host "Press Enter"; exit 1
    }
    Write-OK "Process stopped"
}

# Clean
if ($Clean -and (Test-Path $DIST_DIR)) {
    Write-Step "Cleaning dist/..."
    Remove-Item -Recurse -Force $DIST_DIR
    Write-OK "Cleaned"
}

# Create directories
foreach ($dir in @($DIST_DIR, $DATA_DIR)) {
    if (-not (Test-Path $dir)) { New-Item -ItemType Directory -Force -Path $dir | Out-Null }
}

# Tests
if ($SkipTests) {
    Write-Skip "Tests skipped (-SkipTests)"
} else {
    Write-Step "Running tests..."
    $goTempDir = & $GoExePath env GOTMPDIR 2>$null
    if (-not $goTempDir) { $goTempDir = [System.IO.Path]::GetTempPath() }
    try {
        $null = Get-Command Add-MpPreference -ErrorAction Stop
        Add-MpPreference -ExclusionPath $goTempDir   -ErrorAction SilentlyContinue
        Add-MpPreference -ExclusionPath $PSScriptRoot -ErrorAction SilentlyContinue
    } catch {}
    $testOut = & $GoExePath test ./... -timeout 90s 2>&1
    $testOut | ForEach-Object { Write-Host "    $_" -ForegroundColor DarkGray }
    if ($LASTEXITCODE -ne 0) {
        Write-Fail "Tests failed -- fix before building"; Read-Host "Press Enter"; exit 1
    }
    Write-OK "All tests passed"
}

# Compile
Write-Step "Compiling $BINARY..."
$ldflags = ""
if ($Release) {
    $ldflags = "-s -w"; if (-not $NoGui) { $ldflags += " -H windowsgui" }
    Write-Skip "Mode: Release (stripped$(if (-not $NoGui){', no console'}))"
} else {
    if (-not $NoGui) { $ldflags = "-H windowsgui" }
    Write-Skip "Mode: Debug"
}
$env:GOOS = "windows"; $env:GOARCH = "amd64"; $env:CGO_ENABLED = "0"
$buildArgs = @("build","-o","$DIST_DIR\$BINARY")
if ($ldflags -ne "") { $buildArgs += @("-ldflags",$ldflags) }
$buildArgs += $MAIN_PATH
& $GoExePath @buildArgs
if ($LASTEXITCODE -ne 0) { Write-Fail "Compilation failed"; Read-Host "Press Enter"; exit 1 }
Write-OK "$BINARY compiled ($([math]::Round((Get-Item "$DIST_DIR\$BINARY").Length/1MB,2)) MB)"

# Copy runtime files
Write-Step "Copying runtime files to data/..."
foreach ($f in (Get-ChildItem -Path "." -Filter "geosite-*.bin")) {
    Copy-Item $f.FullName "$DATA_DIR\$($f.Name)" -Force
    Write-Skip "data\$($f.Name)"
}
Copy-Item "routing.json" "$DATA_DIR\routing.json" -Force
Write-OK "data\ ready ($(((Get-ChildItem $DATA_DIR).Count)) files)"

# Placeholders
Write-Step "Checking required files..."

$singBoxDest = "$DIST_DIR\sing-box.exe"
if (-not (Test-Path $singBoxDest)) {
    New-Item -ItemType File -Path $singBoxDest | Out-Null
    Write-Warn "sing-box.exe  placeholder created"
    Write-Skip "  Download: https://github.com/SagerNet/sing-box/releases"
    Write-Skip "  Extract sing-box.exe into dist\"
} elseif ((Get-Item $singBoxDest).Length -eq 0) {
    Write-Warn "sing-box.exe  still a placeholder (0 bytes)"
} else {
    Write-OK "sing-box.exe  present ($([math]::Round((Get-Item $singBoxDest).Length/1MB,1)) MB)"
}

$secretDest = "$DIST_DIR\secret.key"
if (-not (Test-Path $secretDest)) {
    @(
        "# ----------------------------------------------------------------------",
        "# proxy-client -- VLESS connection key",
        "# ----------------------------------------------------------------------",
        "#",
        "# Paste your VLESS link below. Lines starting with # are ignored.",
        "# Example:",
        "#   vless://UUID@HOST:PORT?encryption=none&security=reality&sni=SNI&pbk=KEY&sid=ID&fp=chrome&flow=xtls-rprx-vision",
        "#",
        "# Get your link from your VPN provider or self-hosted server.",
        "# ----------------------------------------------------------------------"
    ) | Set-Content -Path $secretDest -Encoding UTF8
    Write-Warn "secret.key    placeholder created -- open and paste your VLESS link"
} else {
    if ((Get-Content $secretDest -Raw) -match "vless://") {
        Write-OK "secret.key    VLESS link present"
    } else {
        Write-Warn "secret.key    exists but no vless:// found -- check the file"
    }
}

# README.txt
$readmeDest = "$DIST_DIR\README.txt"
$readmeContent = @"
+==================================================================+
|              proxy-client -- Quick Start Guide                   |
+==================================================================+

REQUIREMENTS
  - Windows 10 / 11 (64-bit)
  - Administrator rights (required for TUN network interface)

--- FIRST RUN: 3 STEPS -------------------------------------------

  Step 1. Download sing-box.exe
  --------------------------------
  Go to: https://github.com/SagerNet/sing-box/releases
  Download: sing-box-X.X.X-windows-amd64.zip
  Extract sing-box.exe into this folder (next to proxy-client.exe).

  Step 2. Add your VLESS link
  --------------------------------
  Open secret.key in Notepad.
  Delete all lines starting with # and paste your VLESS link.
  Save the file. Example:

    vless://uuid@host:443?security=reality&sni=example.com&...

  Step 3. Run
  --------------------------------
  Double-click proxy-client.exe
  (or right-click -> Run as Administrator)

  The icon will appear in the system tray (near the clock).
  Open the control panel at: http://localhost:8080

--- FOLDER STRUCTURE ----------------------------------------------

  proxy-client.exe   main application
  sing-box.exe       proxy core (download separately, see Step 1)
  secret.key         your VLESS connection link (keep it private!)

  data\
    routing.json     routing rules (edit via the UI, not manually)
    geosite-*.bin    site category databases for routing

  README.txt         this file

--- USAGE ---------------------------------------------------------

  Tray icon: right-click -> Enable / Disable / Quit
  Web UI:    http://localhost:8080

  In the Rules tab you can add a domain, IP, or .exe process
  and assign it: proxy / direct / block.

--- AUTOSTART -----------------------------------------------------

  Web UI -> Settings -> Start with Windows

--- TROUBLESHOOTING -----------------------------------------------

  "Cannot create a file when that file already exists"
    This is a wintun (TUN driver) issue. The client fixes it
    automatically on the next launch. Wait ~30 seconds.

  Tray icon does not appear
    Make sure you run as Administrator.

  Proxy does not work
    - Check that sing-box.exe is not 0 bytes (see Step 1).
    - Open http://localhost:8080 and check the Log tab.
    - Verify your VLESS link is correct (see Step 2).

  Log files (created next to proxy-client.exe):
    proxy-client.log    main log
    anomaly-*.log       crash diagnostics (created on sing-box errors)

--- UPDATING ------------------------------------------------------

  Replace proxy-client.exe only.
  Do NOT replace data\ or secret.key -- your rules and key are there.

+==================================================================+
"@
$readmeContent | Set-Content -Path $readmeDest -Encoding UTF8
Write-OK "README.txt $(if ((Test-Path $readmeDest)){'updated'}else{'created'})"

# Summary
Write-Host ""
Write-Host "  +--------------------------------------------+" -ForegroundColor Cyan
Write-Host "  |            dist/ contents                  |" -ForegroundColor Cyan
Write-Host "  +--------------------------------------------+" -ForegroundColor Cyan

Get-ChildItem $DIST_DIR -Recurse | Sort-Object FullName | ForEach-Object {
    $rel = $_.FullName.Substring((Resolve-Path $DIST_DIR).Path.Length + 1)
    if ($_.PSIsContainer) {
        Write-Host ("  [{0}/]" -f $rel) -ForegroundColor DarkGray
    } else {
        $size = if ($_.Length -ge 1MB)     { "{0:N1} MB" -f ($_.Length/1MB)   }
                elseif ($_.Length -ge 1KB) { "{0:N1} KB" -f ($_.Length/1KB)   }
                else                       { "{0} B"     -f $_.Length          }
        $col = if ($_.Length -eq 0) { "DarkYellow" } else { "White" }
        Write-Host ("  {0,-44} {1,8}" -f $rel, $size) -ForegroundColor $col
    }
}

Write-Host ""
Write-Host "  Status:" -ForegroundColor Cyan

$sbOK = (Test-Path "$DIST_DIR\sing-box.exe") -and ((Get-Item "$DIST_DIR\sing-box.exe").Length -gt 0)
$skOK = (Test-Path "$DIST_DIR\secret.key")   -and ((Get-Content "$DIST_DIR\secret.key" -Raw) -match "vless://")

if ($sbOK) { Write-Host "    [OK] sing-box.exe  ready" -ForegroundColor Green
} else      { Write-Host "    [!!] sing-box.exe  download needed -> https://github.com/SagerNet/sing-box/releases" -ForegroundColor Yellow }

if ($skOK) { Write-Host "    [OK] secret.key   VLESS link present" -ForegroundColor Green
} else      { Write-Host "    [!!] secret.key   paste your VLESS link into dist\secret.key" -ForegroundColor Yellow }

Write-Host ""
if ($sbOK -and $skOK) {
    Write-Host "  Ready!  Run:  .\dist\proxy-client.exe" -ForegroundColor Green
} else {
    Write-Host "  Complete the steps above, then run:" -ForegroundColor Yellow
    Write-Host "    .\dist\proxy-client.exe" -ForegroundColor Cyan
    Write-Host "  See dist\README.txt for full instructions." -ForegroundColor DarkGray
}
Write-Host ""
Read-Host "Press Enter to close"
