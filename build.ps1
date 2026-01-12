# build.ps1
# Build script for Windows

param(
    [switch]$Release,
    [switch]$Clean
)

Write-Host ""
Write-Host "========================================" -ForegroundColor Cyan
Write-Host "  Proxy Client - Build" -ForegroundColor Cyan
Write-Host "========================================" -ForegroundColor Cyan
Write-Host ""

$BUILD_DIR = "build"
$BINARY_NAME = "proxy-client.exe"
$MAIN_PATH = ".\cmd\proxy-client"

# Clean if requested
if ($Clean) {
    Write-Host "Cleaning build artifacts..." -ForegroundColor Yellow
    if (Test-Path $BUILD_DIR) {
        Remove-Item -Recurse -Force $BUILD_DIR
    }
    Remove-Item -ErrorAction SilentlyContinue config.runtime.json
    Remove-Item -ErrorAction SilentlyContinue coverage.out
    Remove-Item -ErrorAction SilentlyContinue coverage.html
    Write-Host "[OK] Clean complete" -ForegroundColor Green
    Write-Host ""
}

# Create build directory
if (-not (Test-Path $BUILD_DIR)) {
    New-Item -ItemType Directory -Force -Path $BUILD_DIR | Out-Null
}

# Build
Write-Host "Building application..." -ForegroundColor Yellow

if ($Release) {
    Write-Host "  Build type: Release (optimized)" -ForegroundColor Gray
    go build -ldflags="-s -w" -o "$BUILD_DIR\$BINARY_NAME" $MAIN_PATH
} else {
    Write-Host "  Build type: Debug" -ForegroundColor Gray
    go build -v -o "$BUILD_DIR\$BINARY_NAME" $MAIN_PATH
}

if ($LASTEXITCODE -eq 0) {
    Write-Host ""
    Write-Host "[OK] Build complete!" -ForegroundColor Green

    $size = (Get-Item "$BUILD_DIR\$BINARY_NAME").Length / 1MB
    Write-Host "  Output: $BUILD_DIR\$BINARY_NAME" -ForegroundColor Gray
    Write-Host "  Size: $([math]::Round($size, 2)) MB" -ForegroundColor Gray
    Write-Host ""

    Write-Host "To run:" -ForegroundColor Yellow
    Write-Host "  .\$BUILD_DIR\$BINARY_NAME" -ForegroundColor White
} else {
    Write-Host ""
    Write-Host "[ERROR] Build failed!" -ForegroundColor Red
    exit 1
}

Write-Host ""