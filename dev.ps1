# dev.ps1
# Development helper script

param(
    [Parameter(Position=0)]
    [string]$Command = "help"
)

$ErrorActionPreference = "Stop"

function Show-Help {
    Write-Host ""
    Write-Host "========================================" -ForegroundColor Cyan
    Write-Host "  Proxy Client - Dev Tools" -ForegroundColor Cyan
    Write-Host "========================================" -ForegroundColor Cyan
    Write-Host ""
    Write-Host "Available commands:" -ForegroundColor Yellow
    Write-Host ""
    Write-Host "  Build:" -ForegroundColor White
    Write-Host "    build                Build application (debug)" -ForegroundColor Gray
    Write-Host "    build-release        Build application (release)" -ForegroundColor Gray
    Write-Host "    clean                Clean build artifacts" -ForegroundColor Gray
    Write-Host ""
    Write-Host "  Test:" -ForegroundColor White
    Write-Host "    test                 Run all tests" -ForegroundColor Gray
    Write-Host "    test-unit            Run unit tests" -ForegroundColor Gray
    Write-Host "    test-integration     Run integration tests" -ForegroundColor Gray
    Write-Host "    test-coverage        Run tests with coverage" -ForegroundColor Gray
    Write-Host "    test-bench           Run benchmarks" -ForegroundColor Gray
    Write-Host ""
    Write-Host "  Code Quality:" -ForegroundColor White
    Write-Host "    fmt                  Format code" -ForegroundColor Gray
    Write-Host "    vet                  Run go vet" -ForegroundColor Gray
    Write-Host "    lint                 Run linter" -ForegroundColor Gray
    Write-Host ""
    Write-Host "  Dependencies:" -ForegroundColor White
    Write-Host "    deps                 Install dependencies" -ForegroundColor Gray
    Write-Host "    deps-update          Update dependencies" -ForegroundColor Gray
    Write-Host ""
    Write-Host "  Run:" -ForegroundColor White
    Write-Host "    run                  Run application" -ForegroundColor Gray
    Write-Host ""
    Write-Host "  Info:" -ForegroundColor White
    Write-Host "    info                 Show project info" -ForegroundColor Gray
    Write-Host "    help                 Show this help" -ForegroundColor Gray
    Write-Host ""
    Write-Host "Examples:" -ForegroundColor Yellow
    Write-Host "  .\dev.ps1 build" -ForegroundColor White
    Write-Host "  .\dev.ps1 test-coverage" -ForegroundColor White
    Write-Host "  .\dev.ps1 fmt" -ForegroundColor White
    Write-Host ""
}

function Invoke-Build {
    Write-Host "Building application..." -ForegroundColor Yellow
    go build -v -o build\proxy-client.exe .\cmd\proxy-client
}

function Invoke-BuildRelease {
    Write-Host "Building release..." -ForegroundColor Yellow
    if (-not (Test-Path build)) {
        New-Item -ItemType Directory build | Out-Null
    }
    go build -ldflags="-s -w" -o build\proxy-client.exe .\cmd\proxy-client

    if ($LASTEXITCODE -eq 0) {
        $size = (Get-Item build\proxy-client.exe).Length / 1MB
        Write-Host "[OK] Build complete: build\proxy-client.exe ($([math]::Round($size, 2)) MB)" -ForegroundColor Green
    }
}

function Invoke-Clean {
    Write-Host "Cleaning..." -ForegroundColor Yellow
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue build
    Remove-Item -ErrorAction SilentlyContinue config.runtime.json, coverage.out, coverage.html
    Write-Host "[OK] Clean complete" -ForegroundColor Green
}

function Invoke-Test {
    Write-Host "Running all tests..." -ForegroundColor Yellow
    go test -v ./...
}

function Invoke-TestUnit {
    Write-Host "Running unit tests..." -ForegroundColor Yellow
    go test -v -short ./internal/...
}

function Invoke-TestIntegration {
    Write-Host "Running integration tests..." -ForegroundColor Yellow
    go test -v ./tests/...
}

function Invoke-TestCoverage {
    Write-Host "Running tests with coverage..." -ForegroundColor Yellow
    go test -v -coverprofile=coverage.out -covermode=atomic ./...

    if ($LASTEXITCODE -eq 0) {
        go tool cover -html=coverage.out -o coverage.html
        Write-Host ""
        Write-Host "Coverage summary:" -ForegroundColor Cyan
        go tool cover -func=coverage.out | Select-String "total"
        Write-Host ""
        Write-Host "[OK] Coverage report: coverage.html" -ForegroundColor Green
    }
}

function Invoke-TestBench {
    Write-Host "Running benchmarks..." -ForegroundColor Yellow
    go test -bench=. -benchmem ./...
}

function Invoke-Format {
    Write-Host "Formatting code..." -ForegroundColor Yellow
    go fmt ./...
    Write-Host "[OK] Format complete" -ForegroundColor Green
}

function Invoke-Vet {
    Write-Host "Running go vet..." -ForegroundColor Yellow
    go vet ./...
    if ($LASTEXITCODE -eq 0) {
        Write-Host "[OK] Vet complete" -ForegroundColor Green
    }
}

function Invoke-Lint {
    Write-Host "Running linter..." -ForegroundColor Yellow

    if (-not (Get-Command golangci-lint -ErrorAction SilentlyContinue)) {
        Write-Host "[ERROR] golangci-lint not installed" -ForegroundColor Red
        Write-Host "Install: https://golangci-lint.run/usage/install/" -ForegroundColor Yellow
        exit 1
    }

    golangci-lint run ./...
    if ($LASTEXITCODE -eq 0) {
        Write-Host "[OK] Lint complete" -ForegroundColor Green
    }
}

function Invoke-Deps {
    Write-Host "Installing dependencies..." -ForegroundColor Yellow
    go mod download
    go mod tidy
    Write-Host "[OK] Dependencies installed" -ForegroundColor Green
}

function Invoke-DepsUpdate {
    Write-Host "Updating dependencies..." -ForegroundColor Yellow
    go get -u ./...
    go mod tidy
    Write-Host "[OK] Dependencies updated" -ForegroundColor Green
}

function Invoke-Run {
    Write-Host "Running application..." -ForegroundColor Yellow
    go run .\cmd\proxy-client\main.go
}

function Show-Info {
    Write-Host ""
    Write-Host "========================================" -ForegroundColor Cyan
    Write-Host "  Project Info" -ForegroundColor Cyan
    Write-Host "========================================" -ForegroundColor Cyan
    Write-Host ""

    Write-Host "Go version:" -ForegroundColor Yellow
    go version
    Write-Host ""

    Write-Host "Module:" -ForegroundColor Yellow
    Get-Content go.mod | Select-Object -First 1
    Write-Host ""

    Write-Host "Dependencies:" -ForegroundColor Yellow
    go list -m all | Select-Object -Skip 1
    Write-Host ""

    Write-Host "Files:" -ForegroundColor Yellow
    Write-Host "  config.template.json: $(if (Test-Path config.template.json) { 'EXISTS' } else { 'MISSING' })" -ForegroundColor $(if (Test-Path config.template.json) { 'Green' } else { 'Red' })
    Write-Host "  secret.key: $(if (Test-Path secret.key) { 'EXISTS' } else { 'MISSING' })" -ForegroundColor $(if (Test-Path secret.key) { 'Green' } else { 'Red' })
    Write-Host "  xray_core/xray.exe: $(if (Test-Path xray_core/xray.exe) { 'EXISTS' } else { 'MISSING' })" -ForegroundColor $(if (Test-Path xray_core/xray.exe) { 'Green' } else { 'Red' })
    Write-Host ""
}

# Main command dispatcher
switch ($Command.ToLower()) {
    "build" { Invoke-Build }
    "build-release" { Invoke-BuildRelease }
    "clean" { Invoke-Clean }
    "test" { Invoke-Test }
    "test-unit" { Invoke-TestUnit }
    "test-integration" { Invoke-TestIntegration }
    "test-coverage" { Invoke-TestCoverage }
    "test-bench" { Invoke-TestBench }
    "fmt" { Invoke-Format }
    "vet" { Invoke-Vet }
    "lint" { Invoke-Lint }
    "deps" { Invoke-Deps }
    "deps-update" { Invoke-DepsUpdate }
    "run" { Invoke-Run }
    "info" { Show-Info }
    "help" { Show-Help }
    default {
        Write-Host "Unknown command: $Command" -ForegroundColor Red
        Write-Host "Run '.\dev.ps1 help' for available commands" -ForegroundColor Yellow
        exit 1
    }
}