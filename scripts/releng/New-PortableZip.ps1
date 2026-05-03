#Requires -Version 5.1
param(
    [string]$DistDir = "dist",
    [Parameter(Mandatory = $true)][string]$Version,
    [string]$OutputDir = "artifacts"
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

if ($Version -notmatch '^v?\d+\.\d+\.\d+([-.][0-9A-Za-z.-]+)?$') {
    throw "Version must be semantic version, got: $Version"
}
if (-not (Test-Path -LiteralPath $DistDir)) {
    throw "dist dir does not exist: $DistDir"
}

$required = @("SafeSky.exe", "proxy-updater.exe", "README.txt")
foreach ($name in $required) {
    $path = Join-Path $DistDir $name
    if (-not (Test-Path -LiteralPath $path)) {
        throw "required portable file missing: $path"
    }
}

New-Item -ItemType Directory -Force -Path $OutputDir | Out-Null
$versionValue = $Version.TrimStart("v")
$zipPath = Join-Path $OutputDir "proxy-client-$versionValue-windows-amd64.zip"
$staging = Join-Path ([System.IO.Path]::GetTempPath()) ("safesky-portable-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Force -Path $staging | Out-Null

try {
    foreach ($name in $required) {
        Copy-Item -LiteralPath (Join-Path $DistDir $name) -Destination (Join-Path $staging $name) -Force
    }
    $singBox = Join-Path $DistDir "sing-box.exe"
    if (Test-Path -LiteralPath $singBox) {
        Copy-Item -LiteralPath $singBox -Destination (Join-Path $staging "sing-box.exe") -Force
    }
    $dataSrc = Join-Path $DistDir "data"
    if (Test-Path -LiteralPath $dataSrc) {
        $dataDst = Join-Path $staging "data"
        New-Item -ItemType Directory -Force -Path $dataDst | Out-Null
        Get-ChildItem -LiteralPath $dataSrc -Filter "geosite-*.bin" -ErrorAction SilentlyContinue |
            ForEach-Object { Copy-Item -LiteralPath $_.FullName -Destination (Join-Path $dataDst $_.Name) -Force }
    }
    if (Test-Path -LiteralPath $zipPath) { Remove-Item -LiteralPath $zipPath -Force }
    Compress-Archive -Path (Join-Path $staging "*") -DestinationPath $zipPath -CompressionLevel Optimal
    $hashPath = Join-Path $OutputDir "SHA256SUMS.txt"
    $hash = (Get-FileHash -LiteralPath $zipPath -Algorithm SHA256).Hash.ToLowerInvariant()
    "$hash  $(Split-Path -Leaf $zipPath)" | Set-Content -LiteralPath $hashPath -Encoding ASCII
    Write-Host "Wrote $zipPath"
    Write-Host "Wrote $hashPath"
} finally {
    if (Test-Path -LiteralPath $staging) {
        Remove-Item -LiteralPath $staging -Recurse -Force
    }
}
