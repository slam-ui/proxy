#Requires -Version 5.1
<#
.SYNOPSIS
    Сборка и упаковка SafeSky в папку dist/

.DESCRIPTION
    Структура проекта:
      geosite/              исходные .bin файлы категорий сайтов
      templates/            шаблоны для новых пользователей
        routing.json        чистые правила роутинга (без личных доменов)
        secret.key          заготовка для VLESS-ключа
      dist/                 результат сборки (не коммитить личные файлы!)
        SafeSky.exe
        sing-box.exe
        secret.key          <- НЕ перезаписывается если уже содержит vless://
        README.txt
        data/
          routing.json      <- НЕ перезаписывается если уже существует
          geosite-*.bin

    Логика сохранения пользовательских данных:
      - secret.key и data/routing.json НЕ перезаписываются при обычной сборке.
      - Чтобы сбросить к шаблонам -- используйте -Clean или -ResetData.
      - geosite-*.bin копируются из geosite/ всегда (они не редактируются вручную).

.PARAMETER Release
    Компилировать без отладочной информации и без консольного окна.

.PARAMETER Clean
    Полностью удалить dist/ перед сборкой (сбрасывает ВСЁ включая secret.key).

.PARAMETER ResetData
    Сбросить только data/routing.json и secret.key к шаблонам из templates/.

.PARAMETER NoGui
    Оставить консольное окно (удобно для отладки).

.PARAMETER SkipTests
    Пропустить тесты.

.PARAMETER NoPause
    Не ждать нажатия Enter в конце или при ошибке. Удобно для CI/CD и автоматизации.

.PARAMETER GoExePath
    Явный путь к go.exe (определяется автоматически если не указан).

.EXAMPLE
    .\build.ps1                          # Debug-сборка, данные сохраняются
    .\build.ps1 -Release                 # Release для раздачи
    .\build.ps1 -Release -Clean          # Чистая Release-сборка с нуля
    .\build.ps1 -ResetData               # Сбросить routing.json и secret.key к умолчаниям
#>
param(
    [switch]$Release,
    [switch]$Clean,
    [switch]$ResetData,
    [switch]$NoGui,
    [switch]$SkipTests,
    [switch]$NoPause,
    [string]$GoExePath = ""
)

$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

# Paths
$DIST_DIR      = "dist"
$DATA_DIR      = "$DIST_DIR\data"
$GEOSITE_DIR   = "geosite"
$TEMPLATES_DIR = "templates"
$BINARY        = "SafeSky.exe"
$MAIN_PATH     = ".\cmd\proxy-client"

function Write-Banner([string]$text) {
    $w = $text.Length + 4
    $line = "-" * $w
    Write-Host ""
    Write-Host "  +$line+" -ForegroundColor Cyan
    Write-Host "  |  $text  |" -ForegroundColor Cyan
    Write-Host "  +$line+" -ForegroundColor Cyan
    Write-Host ""
}
function Write-Step([string]$text)  { Write-Host "  >> $text" -ForegroundColor Yellow }
function Write-OK([string]$text)    { Write-Host "  OK $text" -ForegroundColor Green }
function Write-Skip([string]$text)  { Write-Host "     $text" -ForegroundColor DarkGray }
function Write-Warn([string]$text)  { Write-Host "  !! $text" -ForegroundColor DarkYellow }
function Write-Fail([string]$text)  { Write-Host ""; Write-Host "  XX $text" -ForegroundColor Red; Write-Host "" }
function Wait-BuildClose {
    if (-not $NoPause) { Read-Host "  Press Enter to close" | Out-Null }
}
function Exit-Build([int]$code) {
    Wait-BuildClose
    exit $code
}

Write-Banner "SafeSky build"

# Admin check
$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole(
    [Security.Principal.WindowsBuiltInRole]::Administrator)

if (-not $isAdmin) {
    Write-Step "Requesting Administrator rights..."
    $argList = @("-NoExit", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "`"$PSCommandPath`"")
    if ($GoExePath) { $argList += @("-GoExePath", "`"$GoExePath`"") }
    if ($Release)   { $argList += "-Release"   }
    if ($Clean)     { $argList += "-Clean"     }
    if ($ResetData) { $argList += "-ResetData" }
    if ($NoGui)     { $argList += "-NoGui"     }
    if ($SkipTests) { $argList += "-SkipTests" }
    if ($NoPause)   { $argList += "-NoPause"   }
    Start-Process powershell -Verb RunAs -ArgumentList $argList
    exit 0
}

# Find Go
Write-Step "Locating Go toolchain..."
if ($GoExePath -eq "") {
    try { $GoExePath = (Get-Command go -ErrorAction Stop).Source } catch {}
    if (-not $GoExePath) {
        $candidates = @(
            "C:\Program Files\Go\bin\go.exe",
            "C:\Go\bin\go.exe",
            "$env:LOCALAPPDATA\Programs\Go\bin\go.exe"
        )
        $sdkBase = Join-Path $env:USERPROFILE "sdk"
        if (Test-Path $sdkBase) {
            Get-ChildItem -Path $sdkBase -Directory -Filter "go*" |
                Sort-Object Name -Descending |
                ForEach-Object { $candidates += (Join-Path $_.FullName "bin\go.exe") }
        }
        foreach ($c in $candidates) { if (Test-Path $c) { $GoExePath = $c; break } }
    }
}
if (-not $GoExePath -or -not (Test-Path $GoExePath)) {
    Write-Fail "go.exe not found. Install from https://go.dev/dl/"
    Exit-Build 1
}
Write-OK "$(& $GoExePath version)"

# Stop running instance
$running = Get-Process -Name "SafeSky" -ErrorAction SilentlyContinue
if ($running) {
    Write-Step "Stopping running SafeSky.exe..."
    try { Invoke-RestMethod -Uri "http://localhost:8080/api/quit" -Method POST -TimeoutSec 3 | Out-Null } catch {}
    $waited = 0
    while ($waited -lt 5000) {
        if (-not (Get-Process -Name "SafeSky" -ErrorAction SilentlyContinue)) { break }
        Start-Sleep -Milliseconds 300; $waited += 300
    }
    if (Get-Process -Name "SafeSky" -ErrorAction SilentlyContinue) {
        Get-Process -Name "SafeSky" -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
        Start-Sleep -Milliseconds 800
    }
    if (Get-Process -Name "SafeSky" -ErrorAction SilentlyContinue) {
        Write-Fail "Could not stop SafeSky.exe -- close it manually and retry"
        Exit-Build 1
    }
    Write-OK "Process stopped"
}

# Clean (preserve sing-box.exe — it's 19 MB and requires GitHub access to re-download)
if ($Clean -and (Test-Path $DIST_DIR)) {
    Write-Step "Cleaning dist/..."
    $singBoxBackup = $null
    $singBoxSrc = "$DIST_DIR\sing-box.exe"
    if ((Test-Path $singBoxSrc) -and (Get-Item $singBoxSrc).Length -gt 0) {
        $singBoxBackup = [System.IO.Path]::GetTempFileName()
        Copy-Item $singBoxSrc $singBoxBackup -Force
        Write-Skip "sing-box.exe backed up (will be restored after clean)"
    }
    Remove-Item -Recurse -Force $DIST_DIR
    Write-OK "dist/ removed"
    if ($singBoxBackup) {
        New-Item -ItemType Directory -Force -Path $DIST_DIR | Out-Null
        Copy-Item $singBoxBackup "$DIST_DIR\sing-box.exe" -Force
        Remove-Item $singBoxBackup -Force -ErrorAction SilentlyContinue
        Write-OK "sing-box.exe restored from backup"
    }
}

# Create dirs
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
    $testOut = & $GoExePath test ./cmd/... -timeout 120s 2>&1
    $cmdExit = $LASTEXITCODE
    $testOut += & $GoExePath test -race ./internal/... -timeout 120s 2>&1
    $internalExit = $LASTEXITCODE
    $testOut | ForEach-Object { Write-Host "     $_" -ForegroundColor DarkGray }
    if ($cmdExit -ne 0 -or $internalExit -ne 0) {
        Write-Fail "Tests failed -- fix before building"
        Exit-Build 1
    }
    Write-OK "All tests passed"
}

# Embed icon + version info into .syso via goversioninfo
Write-Step "Embedding icon and version info into .exe..."

$goversioninfoExe = $null
$goviCandidates = @(
    "$env:USERPROFILE\go\bin\goversioninfo.exe",
    "$env:GOPATH\bin\goversioninfo.exe",
    "C:\Go\bin\goversioninfo.exe"
)
foreach ($c in $goviCandidates) {
    if (Test-Path $c) { $goversioninfoExe = $c; break }
}
if (-not $goversioninfoExe) {
    try { $goversioninfoExe = (Get-Command goversioninfo -ErrorAction Stop).Source } catch {}
}

if (-not $goversioninfoExe) {
    Write-Skip "goversioninfo not found -- installing..."
    $goviOut = & $GoExePath install "github.com/josephspurrier/goversioninfo/cmd/goversioninfo@latest" 2>&1
    $goviInstallExit = $LASTEXITCODE
    $goviOut | ForEach-Object { Write-Host "     $_" -ForegroundColor DarkGray }
    if ($goviInstallExit -eq 0) {
        foreach ($c in $goviCandidates) {
            if (Test-Path $c) { $goversioninfoExe = $c; break }
        }
        if ($goversioninfoExe) {
            Write-OK "goversioninfo installed: $goversioninfoExe"
        } else {
            Write-Warn "goversioninfo installed but exe not found -- check GOPATH/bin"
        }
    } else {
        Write-Warn "goversioninfo install failed (exit $goviInstallExit)"
    }
} else {
    Write-Skip "goversioninfo: $goversioninfoExe"
}

$viJson  = "$MAIN_PATH\versioninfo.json"
$icoPath = "app_icon.ico"

if ($goversioninfoExe -and (Test-Path $viJson) -and (Test-Path $icoPath)) {
    Push-Location $MAIN_PATH
    $oldErrorActionPreference = $ErrorActionPreference
    try {
        # Some goversioninfo builds print usage to stderr and exit non-zero for -h.
        # Capture that output without letting $ErrorActionPreference=Stop abort the build.
        $ErrorActionPreference = "Continue"
        $goviHelp = (& $goversioninfoExe -h 2>&1) | Out-String
    } finally {
        $ErrorActionPreference = $oldErrorActionPreference
    }
    if ($goviHelp -match "-goarch") {
        & $goversioninfoExe -icon "..\..\app_icon.ico" -o "rsrc_windows_amd64.syso" -goarch amd64 "versioninfo.json"
    } else {
        & $goversioninfoExe -64 -icon "..\..\app_icon.ico" -o "rsrc_windows_amd64.syso" "versioninfo.json"
    }
    $goviExit = $LASTEXITCODE
    Pop-Location
    if ($goviExit -eq 0) {
        $sysoKB = [math]::Round((Get-Item "$MAIN_PATH\rsrc_windows_amd64.syso").Length / 1KB, 1)
        Write-OK "rsrc_windows_amd64.syso regenerated ($sysoKB KB) -- icon embedded"
    } else {
        Write-Warn "goversioninfo exited $goviExit -- .exe may show wrong icon"
    }
} elseif (-not $goversioninfoExe) {
    Write-Warn "goversioninfo unavailable -- .exe icon not updated"
    Write-Warn "  Run: go install github.com/josephspurrier/goversioninfo/cmd/goversioninfo@latest"
} elseif (-not (Test-Path $icoPath)) {
    Write-Warn "app_icon.ico not found in project root -- .exe icon not updated"
} else {
    Write-Warn "versioninfo.json not found in $MAIN_PATH -- .exe icon not updated"
}

# Compile
Write-Step "Compiling $BINARY..."
$ldflags = ""
if ($Release) {
    $ldflags = "-s -w"
    if (-not $NoGui) { $ldflags += " -H windowsgui" }
    Write-Skip "Mode: Release (stripped$(if (-not $NoGui){', no console window'}))"
} else {
    if (-not $NoGui) { $ldflags = "-H windowsgui" }
    Write-Skip "Mode: Debug"
}
$env:GOOS = "windows"; $env:GOARCH = "amd64"; $env:CGO_ENABLED = "0"
$buildArgs = @("build", "-o", "$DIST_DIR\$BINARY")
if ($ldflags -ne "") { $buildArgs += @("-ldflags", $ldflags) }
$buildArgs += $MAIN_PATH
& $GoExePath @buildArgs
if ($LASTEXITCODE -ne 0) { Write-Fail "Compilation failed"; Exit-Build 1 }
$sizeMB = [math]::Round((Get-Item "$DIST_DIR\$BINARY").Length / 1MB, 2)
Write-OK "$BINARY  ($sizeMB MB)"

# Сбрасываем кэш иконок Explorer чтобы новая иконка .exe отображалась сразу в проводнике.
# ie4uinit.exe -show — стандартный механизм Windows 10/11.
Write-Step "Refreshing Explorer icon cache..."
try {
    $iconCachePath = Join-Path $env:LOCALAPPDATA "IconCache.db"
    # SHChangeNotify через PowerShell — сигнализируем Explorer об изменении ассоциаций файлов
    $typeDef = @"
using System;
using System.Runtime.InteropServices;
public class ShellNotify {
    [DllImport("shell32.dll")]
    public static extern void SHChangeNotify(int wEventId, uint uFlags, IntPtr dwItem1, IntPtr dwItem2);
}
"@
    Add-Type -TypeDefinition $typeDef -ErrorAction SilentlyContinue
    [ShellNotify]::SHChangeNotify(0x08000000, 0x0000, [IntPtr]::Zero, [IntPtr]::Zero)
    Write-OK "Icon cache refresh signal sent"
} catch {
    Write-Skip "Icon cache refresh skipped (not critical)"
}

# Geosite: always refresh from geosite/
Write-Step "Copying geosite databases..."
if (-not (Test-Path $GEOSITE_DIR)) {
    Write-Warn "geosite/ folder not found -- skipping"
} else {
    $bins = Get-ChildItem -Path $GEOSITE_DIR -Filter "geosite-*.bin"
    if ($bins.Count -eq 0) {
        Write-Warn "No geosite-*.bin files found in geosite/"
    } else {
        foreach ($f in $bins) {
            Copy-Item $f.FullName "$DATA_DIR\$($f.Name)" -Force
            Write-Skip "data\$($f.Name)  ($([math]::Round($f.Length/1KB,1)) KB)"
        }
        Write-OK "$($bins.Count) geosite databases copied"
    }
}

# routing.json: preserve user's file, copy template only if missing or -ResetData
Write-Step "Checking data\routing.json..."
$routingDest     = "$DATA_DIR\routing.json"
$routingTemplate = "$TEMPLATES_DIR\routing.json"
$needRouting     = $ResetData -or -not (Test-Path $routingDest)

if (-not (Test-Path $routingTemplate)) {
    Write-Warn "templates\routing.json not found -- skipping"
} elseif ($needRouting) {
    Copy-Item $routingTemplate $routingDest -Force
    if ($ResetData) { Write-OK "data\routing.json  reset to defaults (-ResetData)" }
    else            { Write-OK "data\routing.json  created from template" }
} else {
    Write-Skip "data\routing.json  preserved  (use -ResetData to reset to defaults)"
}

# sing-box.exe placeholder
Write-Step "Checking sing-box.exe..."
$singBoxDest = "$DIST_DIR\sing-box.exe"
if (-not (Test-Path $singBoxDest)) {
    New-Item -ItemType File -Path $singBoxDest | Out-Null
    Write-Warn "sing-box.exe  placeholder created  (auto-downloaded on first run)"
} elseif ((Get-Item $singBoxDest).Length -eq 0) {
    Write-Skip "sing-box.exe  placeholder  (will be downloaded on first run)"
} else {
    $sbMB = [math]::Round((Get-Item $singBoxDest).Length / 1MB, 1)
    Write-OK "sing-box.exe  present ($sbMB MB)"
}

# secret.key: preserve user's file, copy template only if missing or -ResetData
Write-Step "Checking secret.key..."
$secretDest     = "$DIST_DIR\secret.key"
$secretTemplate = "$TEMPLATES_DIR\secret.key"
$needSecret     = $ResetData -or -not (Test-Path $secretDest)

if ($needSecret) {
    if (Test-Path $secretTemplate) {
        Copy-Item $secretTemplate $secretDest -Force
    } else {
        @(
            "# ------------------------------------------------------------------",
            "# SafeSky  --  VLESS connection key",
            "# ------------------------------------------------------------------",
            "#",
            "# Paste your VLESS link below (lines starting with # are ignored).",
            "# ------------------------------------------------------------------"
        ) | Set-Content -Path $secretDest -Encoding UTF8
    }
    if ($ResetData) { Write-OK "secret.key  reset to template (-ResetData)" }
    else            { Write-OK "secret.key  created from template" }
} else {
    $hasVless = (Get-Content $secretDest -Raw) -match "vless://"
    if ($hasVless) { Write-OK "secret.key  VLESS link present (preserved)" }
    else           { Write-Warn "secret.key  exists but no vless:// found -- open and paste your link" }
}

# README.txt: always regenerate (contains no personal data)
$readmeDest = "$DIST_DIR\README.txt"
@"
+------------------------------------------------------------------+
|                 SafeSky  --  Quick Start Guide                   |
+------------------------------------------------------------------+

  Requirements
  ------------
  * Windows 10 / 11  (64-bit)
  * Administrator rights  (needed for TUN network interface)


  First run -- 2 steps
  --------------------

  1. Add your VLESS link
     Open secret.key in Notepad, delete the comment lines
     and paste your VLESS link. Save the file.

     Example:
       vless://UUID@HOST:443?security=reality&sni=www.microsoft.com
                &pbk=PUBLIC_KEY&sid=SHORT_ID&fp=chrome
                &flow=xtls-rprx-vision

     Get your link from your server's control panel (e.g. Remnawave).

  2. Run SafeSky.exe
     Right-click -> Run as Administrator  (or just double-click --
     it will request elevation automatically).

     A tray icon will appear near the clock.
     Open the control panel: http://localhost:8080


  Folder layout
  -------------
  SafeSky.exe         main application
  sing-box.exe        proxy core  (auto-downloaded on first run)
  secret.key          your VLESS link  (keep it private!)
  README.txt          this file

  data\
    routing.json      routing rules  (edit in the UI, not manually)
    geosite-*.bin     site category databases


  Usage
  -----
  Tray icon   right-click -> Enable / Disable / Quit
  Web UI      http://localhost:8080

  In the Rules tab you can add domains, IPs, or .exe processes
  and assign them:  proxy  /  direct  /  block.


  Autostart
  ---------
  Web UI -> Settings -> Start with Windows


  Updating
  --------
  Replace SafeSky.exe only.
  Do NOT replace data\ or secret.key -- your rules and key are there.


  Troubleshooting
  ---------------
  Tray icon does not appear
    -> Make sure you run as Administrator.

  Proxy does not work
    -> Open http://localhost:8080 and check the Log tab.
    -> Verify your VLESS link in secret.key.
    -> If sing-box.exe is 0 bytes -- delete it and restart
       (it will be re-downloaded automatically).

  "Cannot create a file when that file already exists"
    -> Wintun driver issue. The client fixes it automatically.
       Wait ~30 seconds and try again.

  Log files (created next to SafeSky.exe):
    safesky.log           main log
    anomaly-*.log         crash diagnostics

+------------------------------------------------------------------+
"@ | Set-Content -Path $readmeDest -Encoding UTF8
Write-OK "README.txt  updated"

# Summary
Write-Host ""
Write-Host "  +-----------------------------------------------------+" -ForegroundColor Cyan
Write-Host "  |  dist/ contents                                     |" -ForegroundColor Cyan
Write-Host "  +-----------------------------------------------------+" -ForegroundColor Cyan

Get-ChildItem $DIST_DIR -Recurse | Sort-Object FullName | ForEach-Object {
    $rel = $_.FullName.Substring((Resolve-Path $DIST_DIR).Path.Length + 1)
    if ($_.PSIsContainer) {
        Write-Host ("  [{0}\]" -f $rel) -ForegroundColor DarkGray
    } else {
        $size = if ($_.Length -ge 1MB)     { "{0:N1} MB" -f ($_.Length / 1MB) }
                elseif ($_.Length -ge 1KB) { "{0:N1} KB" -f ($_.Length / 1KB) }
                else                       { "{0} B"     -f $_.Length          }
        $col = if ($_.Length -eq 0) { "DarkYellow" } else { "White" }
        Write-Host ("  {0,-48} {1,8}" -f $rel, $size) -ForegroundColor $col
    }
}

Write-Host ""
Write-Host "  +-----------------------------------------------------+" -ForegroundColor Cyan
Write-Host "  |  Status                                             |" -ForegroundColor Cyan
Write-Host "  +-----------------------------------------------------+" -ForegroundColor Cyan

$sbOK = (Test-Path "$DIST_DIR\sing-box.exe") -and ((Get-Item "$DIST_DIR\sing-box.exe").Length -gt 0)
$skOK = (Test-Path "$DIST_DIR\secret.key")   -and ((Get-Content "$DIST_DIR\secret.key" -Raw) -match "vless://")

if ($sbOK) { Write-Host "  [OK] sing-box.exe  ready" -ForegroundColor Green }
else        { Write-Host "  [..] sing-box.exe  will be downloaded automatically on first run" -ForegroundColor DarkGray }

if ($skOK) { Write-Host "  [OK] secret.key    VLESS link present" -ForegroundColor Green }
else        { Write-Host "  [!!] secret.key    open and paste your VLESS link" -ForegroundColor Yellow }

Write-Host ""
if ($skOK) {
    Write-Host "  Ready!  Run:  .\dist\SafeSky.exe" -ForegroundColor Green
} else {
    Write-Host "  Next: open dist\secret.key and paste your VLESS link." -ForegroundColor Yellow
    Write-Host "  Then: .\dist\SafeSky.exe" -ForegroundColor Cyan
    Write-Host "  Docs: dist\README.txt" -ForegroundColor DarkGray
}
Write-Host ""
Wait-BuildClose
