#Requires -Version 5.1
param(
    [Parameter(Mandatory = $true)][string]$Version,
    [Parameter(Mandatory = $true)][ValidateSet("stable", "beta", "dev")][string]$Channel,
    [Parameter(Mandatory = $true)][string]$ArtifactPath,
    [Parameter(Mandatory = $true)][string]$DownloadUrl,
    [Parameter(Mandatory = $true)][string]$OutputPath,
    [string]$ChangelogUrl = "",
    [string]$MinVersionForDirectUpdate = "0.0.0",
    [switch]$Critical,
    [switch]$ForceDowngrade
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

if (-not (Test-Path -LiteralPath $ArtifactPath)) {
    throw "artifact does not exist: $ArtifactPath"
}
if ($Version -notmatch '^v?\d+\.\d+\.\d+([-.][0-9A-Za-z.-]+)?$') {
    throw "Version must be semantic version, got: $Version"
}
if ($DownloadUrl -notmatch '^https://') {
    throw "DownloadUrl must be https"
}
if ($ChangelogUrl -and $ChangelogUrl -notmatch '^https://') {
    throw "ChangelogUrl must be https"
}

$artifact = Get-Item -LiteralPath $ArtifactPath
$hash = (Get-FileHash -LiteralPath $artifact.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
$versionValue = $Version.TrimStart("v")

$metadata = [ordered]@{
    channel                       = $Channel
    version                       = $versionValue
    released_at                   = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
    min_version_for_direct_update = $MinVersionForDirectUpdate
    download_url                  = $DownloadUrl
    sha256                        = $hash
    size_bytes                    = $artifact.Length
    critical                      = [bool]$Critical
}
if ($ChangelogUrl) { $metadata.changelog_url = $ChangelogUrl }
if ($ForceDowngrade) { $metadata.force_downgrade = $true }

$outDir = Split-Path -Parent $OutputPath
if ($outDir) { New-Item -ItemType Directory -Force -Path $outDir | Out-Null }
$metadata | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath $OutputPath -Encoding UTF8
Write-Host "Wrote $OutputPath"
