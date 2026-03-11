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
