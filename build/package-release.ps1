#Requires -Version 5.1
<#
.SYNOPSIS
    Builds SafeSky release artifacts for GitHub Releases and update metadata.

.DESCRIPTION
    Produces:
      dist/release/artifacts/SafeSky-<version>-windows-amd64.exe
      dist/release/artifacts/proxy-client-<version>-windows-amd64.zip
      dist/release/artifacts/proxy-client-<version>-windows-amd64.msi when WiX is available
      dist/release/artifacts/proxy-updater-<version>-windows-amd64.exe
      dist/release/artifacts/version.<channel>.json
      dist/release/artifacts/SHA256SUMS.txt
#>
param(
    [string]$Version = "",
    [ValidateSet("stable", "beta", "dev")]
    [string]$Channel = "",
    [string]$OutputDir = "dist\release",
    [string]$UpdateBaseURL = "",
    [switch]$SkipMSI
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest
$ProgressPreference = "SilentlyContinue"

$RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
Set-Location $RepoRoot

function Invoke-Native {
    param([Parameter(Mandatory = $true)][string]$File, [Parameter(ValueFromRemainingArguments = $true)][string[]]$Args)
    & $File @Args
    if ($LASTEXITCODE -ne 0) {
        throw "$File exited with code $LASTEXITCODE"
    }
}

function Get-GitValue([string[]]$Args, [string]$Fallback) {
    try {
        $value = (& git @Args 2>$null | Select-Object -First 1).Trim()
        if ($value) { return $value }
    } catch {}
    return $Fallback
}

if (-not $Version) {
    $Version = Get-GitValue -Args @("describe", "--tags", "--always") -Fallback "0.0.0-dev"
}
$Version = $Version.Trim()
$VersionNoPrefix = $Version.TrimStart("v")
if (-not $Channel) {
    $Channel = if ($VersionNoPrefix -match "-") { "beta" } else { "stable" }
}

$Commit = Get-GitValue -Args @("rev-parse", "HEAD") -Fallback "unknown"
$BuildTime = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
$ArtifactsDir = Join-Path $RepoRoot $OutputDir
$StageDir = Join-Path $ArtifactsDir "stage\SafeSky"
$OutDir = Join-Path $ArtifactsDir "artifacts"

if (Test-Path $ArtifactsDir) {
    Remove-Item -LiteralPath $ArtifactsDir -Recurse -Force
}
New-Item -ItemType Directory -Force -Path $StageDir, $OutDir | Out-Null

$MainExe = Join-Path $StageDir "SafeSky.exe"
$UpdaterExe = Join-Path $StageDir "proxy-updater.exe"
$VersionFlags = "-X proxyclient/internal/version.Version=$Version -X proxyclient/internal/version.Commit=$Commit -X proxyclient/internal/version.BuildTime=$BuildTime"

Write-Host "Building SafeSky $Version ($Channel)"
Invoke-Native go build -trimpath -ldflags "-s -w -H windowsgui $VersionFlags" -o $MainExe .\cmd\proxy-client
Invoke-Native go build -trimpath -ldflags "-s -w" -o $UpdaterExe .\cmd\proxy-updater

foreach ($file in @("sing-box.exe", "wintun.dll", "app_icon.ico", "LICENSE")) {
    if (Test-Path $file) {
        Copy-Item -LiteralPath $file -Destination $StageDir -Force
    }
}

@"
SafeSky $Version

Run SafeSky.exe. User data is created next to the executable and is not bundled
with release artifacts. The MSI installs per-user into LocalAppData\SafeSky.
"@ | Set-Content -Path (Join-Path $StageDir "README.txt") -Encoding UTF8

$DataDir = Join-Path $StageDir "data"
New-Item -ItemType Directory -Force -Path $DataDir | Out-Null
if (Test-Path "templates\routing.json") {
    Copy-Item "templates\routing.json" (Join-Path $DataDir "routing.json") -Force
}
if (Test-Path "geosite") {
    Get-ChildItem -Path "geosite" -Filter "geosite-*.bin" -File |
        Copy-Item -Destination $DataDir -Force
}

$MainAsset = Join-Path $OutDir "SafeSky-$VersionNoPrefix-windows-amd64.exe"
$UpdaterAsset = Join-Path $OutDir "proxy-updater-$VersionNoPrefix-windows-amd64.exe"
$ZipAsset = Join-Path $OutDir "proxy-client-$VersionNoPrefix-windows-amd64.zip"
$MsiAsset = Join-Path $OutDir "proxy-client-$VersionNoPrefix-windows-amd64.msi"

Copy-Item -LiteralPath $MainExe -Destination $MainAsset -Force
Copy-Item -LiteralPath $UpdaterExe -Destination $UpdaterAsset -Force
Compress-Archive -Path (Join-Path $StageDir "*") -DestinationPath $ZipAsset -Force

if (-not $SkipMSI) {
    $wix = Get-Command wix -ErrorAction SilentlyContinue
    if ($wix) {
        Invoke-Native $wix.Source build .\build\installer\installer.wxs -d "Version=$VersionNoPrefix" -d "SourceDir=$StageDir" -out $MsiAsset
    } else {
        Write-Warning "WiX CLI not found; MSI skipped. Install with: dotnet tool install --global wix"
    }
}

if (-not $UpdateBaseURL) {
    if ($env:GITHUB_REPOSITORY -and $env:GITHUB_REF_NAME) {
        $UpdateBaseURL = "https://github.com/$($env:GITHUB_REPOSITORY)/releases/download/$($env:GITHUB_REF_NAME)"
    } else {
        $UpdateBaseURL = "https://example.com/safesky"
    }
}
$UpdateBaseURL = $UpdateBaseURL.TrimEnd("/")

$PayloadForUpdate = $MainAsset
$payloadInfo = Get-Item -LiteralPath $PayloadForUpdate
$payloadHash = (Get-FileHash -Path $PayloadForUpdate -Algorithm SHA256).Hash.ToLowerInvariant()
$metadata = [ordered]@{
    channel = $Channel
    version = $VersionNoPrefix
    released_at = $BuildTime
    min_version_for_direct_update = "0.7.0"
    download_url = "$UpdateBaseURL/$([IO.Path]::GetFileName($PayloadForUpdate))"
    sha256 = $payloadHash
    size_bytes = $payloadInfo.Length
    changelog_url = "$UpdateBaseURL/CHANGELOG.md"
    critical = $false
    force_downgrade = $false
}
$metadataPath = Join-Path $OutDir "version.$Channel.json"
$metadata | ConvertTo-Json -Depth 5 | Set-Content -Path $metadataPath -Encoding UTF8

Copy-Item -LiteralPath "CHANGELOG.md" -Destination (Join-Path $OutDir "CHANGELOG.md") -Force

$sumLines = Get-ChildItem -Path $OutDir -File |
    Where-Object { $_.Name -ne "SHA256SUMS.txt" } |
    Sort-Object Name |
    ForEach-Object {
        "{0}  {1}" -f (Get-FileHash -Path $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant(), $_.Name
    }
$sumLines | Set-Content -Path (Join-Path $OutDir "SHA256SUMS.txt") -Encoding ASCII

Write-Host "Release artifacts:"
Get-ChildItem -Path $OutDir -File | Sort-Object Name | ForEach-Object {
    Write-Host ("  {0} ({1:N1} KB)" -f $_.Name, ($_.Length / 1KB))
}
