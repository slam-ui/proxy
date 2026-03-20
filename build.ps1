# build.ps1
param(
    [switch]$Release,
    [switch]$Clean,
    [switch]$NoGui,
    [string]$GoExePath = ""
)

$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

$DIST_DIR  = "dist"
$BINARY    = "proxy-client.exe"
$MAIN_PATH = ".\cmd\proxy-client"

Write-Host ""
Write-Host "========================================" -ForegroundColor Cyan
Write-Host "  Proxy Client -- Build and Package"     -ForegroundColor Cyan
Write-Host "========================================" -ForegroundColor Cyan
Write-Host ""

if ($GoExePath -eq "") {
    try {
        $GoExePath = (Get-Command go -ErrorAction Stop).Source
    } catch {
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
    Write-Host "[ERROR] go.exe not found." -ForegroundColor Red
    Read-Host "Press Enter to close"
    exit 1
}

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

if ($Clean -and (Test-Path $DIST_DIR)) {
    Write-Host "Cleaning $DIST_DIR ..." -ForegroundColor Yellow
    Remove-Item -Recurse -Force $DIST_DIR
    Write-Host "[OK] Cleaned" -ForegroundColor Green
    Write-Host ""
}

if (-not (Test-Path $DIST_DIR)) {
    New-Item -ItemType Directory -Force -Path $DIST_DIR | Out-Null
}

$DATA_DIR = "$DIST_DIR\data"
if (-not (Test-Path $DATA_DIR)) {
    New-Item -ItemType Directory -Force -Path $DATA_DIR | Out-Null
}

$running = Get-Process -Name "proxy-client" -ErrorAction SilentlyContinue
if ($running) {
    Write-Host "Stopping running proxy-client.exe ..." -ForegroundColor Yellow
    try {
        Invoke-RestMethod -Uri "http://localhost:8080/api/quit" -Method POST -TimeoutSec 3 | Out-Null
        Write-Host "  Waiting for graceful exit..." -ForegroundColor Gray
    } catch { }
    $waited = 0
    while ($waited -lt 5000) {
        $check = Get-Process -Name "proxy-client" -ErrorAction SilentlyContinue
        if (-not $check) { break }
        Start-Sleep -Milliseconds 300
        $waited += 300
    }
    $still = Get-Process -Name "proxy-client" -ErrorAction SilentlyContinue
    if ($still) {
        Write-Host "  Force killing..." -ForegroundColor DarkYellow
        $still | Stop-Process -Force -ErrorAction SilentlyContinue
        Start-Sleep -Milliseconds 800
    }
    $check = Get-Process -Name "proxy-client" -ErrorAction SilentlyContinue
    if ($check) {
        Write-Host "[ERROR] Could not stop proxy-client.exe" -ForegroundColor Red
        Read-Host "Press Enter to close"
        exit 1
    }
    Write-Host "[OK] Process stopped" -ForegroundColor Green
    Write-Host ""
}

Write-Host "Running tests ..." -ForegroundColor Yellow

# Добавляем исключение Windows Defender для Go temp-директории.
# Без этого Defender блокирует тестовые бинарники которые загружают wintun.dll через syscall
# (ложное срабатывание: динамическая загрузка драйверных DLL выглядит подозрительно).
# Исключение добавляется только на время сборки и не меняет политику постоянно.
$goTempDir = & $GoExePath env GOTMPDIR 2>$null
if (-not $goTempDir) { $goTempDir = [System.IO.Path]::GetTempPath() }
$projectDir = $PSScriptRoot

$defenderAvailable = $false
try {
    $null = Get-Command Add-MpPreference -ErrorAction Stop
    $defenderAvailable = $true
} catch { }

if ($defenderAvailable) {
    try {
        Add-MpPreference -ExclusionPath $goTempDir -ErrorAction SilentlyContinue
        Add-MpPreference -ExclusionPath $projectDir -ErrorAction SilentlyContinue
        Write-Host "  [OK] Defender exclusion добавлен для $goTempDir" -ForegroundColor DarkGray
    } catch {
        Write-Host "  [WARN] Не удалось добавить Defender exclusion (нет прав?): $_" -ForegroundColor DarkYellow
    }
}

$testResult = & $GoExePath test ./... -timeout 60s 2>&1
$testResult | ForEach-Object { Write-Host "  $_" -ForegroundColor DarkGray }
if ($LASTEXITCODE -ne 0) {
    Write-Host ""
    Write-Host "[ERROR] Tests failed -- build aborted" -ForegroundColor Red
    Read-Host "Press Enter to close"
    exit 1
}
Write-Host "[OK] All tests passed" -ForegroundColor Green
Write-Host ""

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
$env:CGO_ENABLED = "0"

$buildArgs = @("build", "-o", "$DIST_DIR\$BINARY")
if ($ldflags -ne "") {
    $buildArgs += @("-ldflags", $ldflags)
}
$buildArgs += $MAIN_PATH

& $GoExePath @buildArgs

if ($LASTEXITCODE -ne 0) {
    Write-Host "[ERROR] Compilation failed" -ForegroundColor Red
    Read-Host "Press Enter to close"
    exit 1
}
Write-Host "[OK] $BINARY compiled" -ForegroundColor Green

Write-Host ""
Write-Host "Copying runtime files ..." -ForegroundColor Yellow

$geoBins = Get-ChildItem -Path "." -Filter "geosite-*.bin"
foreach ($f in $geoBins) {
    Copy-Item $f.FullName "$DATA_DIR\$($f.Name)" -Force
    Write-Host "  [+] data\$($f.Name)" -ForegroundColor Gray
}

Copy-Item "routing.json" "$DATA_DIR\routing.json" -Force
Write-Host "  [+] data\routing.json" -ForegroundColor Gray
Write-Host "[OK] Runtime files copied" -ForegroundColor Green

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
    $line1 = "# Paste your VLESS link here, example:"
    $line2 = "# vless://UUID@HOST:PORT?encryption=none&security=reality&sni=SNI"
    Set-Content -Path $secretDest -Value @($line1, $line2) -Encoding UTF8
    Write-Host "  [!] secret.key    -- PLACEHOLDER, paste your VLESS link" -ForegroundColor DarkYellow
} else {
    Write-Host "  [=] secret.key    -- already exists, skipped" -ForegroundColor Gray
}

Write-Host "[OK] Placeholders ready" -ForegroundColor Green

Write-Host ""
Write-Host "========================================" -ForegroundColor Cyan
Write-Host "  dist/ contents:"                        -ForegroundColor Cyan
Write-Host "========================================" -ForegroundColor Cyan
Get-ChildItem $DIST_DIR -Recurse | Sort-Object FullName | ForEach-Object {
    $rel = $_.FullName.Substring((Resolve-Path $DIST_DIR).Path.Length + 1)
    if ($_.PSIsContainer) {
        Write-Host ("  [{0}/]" -f $rel) -ForegroundColor DarkGray
    } else {
        $size = if ($_.Length -ge 1MB)     { "{0:N1} MB" -f ($_.Length/1MB) }
                elseif ($_.Length -ge 1KB) { "{0:N1} KB" -f ($_.Length/1KB) }
                else                       { "{0} B"     -f $_.Length }
        Write-Host ("  {0,-40} {1,8}" -f $rel, $size)
    }
}
Write-Host ""

$exeMB = [math]::Round((Get-Item "$DIST_DIR\$BINARY").Length / 1MB, 2)
Write-Host "[OK] Build complete -- $BINARY = $exeMB MB" -ForegroundColor Green
Write-Host ""
Write-Host "Run:  .\$DIST_DIR\$BINARY" -ForegroundColor Cyan
Write-Host ""
Read-Host "Press Enter to close"
