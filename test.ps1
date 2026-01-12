# test.ps1
# Test runner script for Windows

param(
    [string]$Type = "all"
)

Write-Host ""
Write-Host "========================================" -ForegroundColor Cyan
Write-Host "  Running Tests" -ForegroundColor Cyan
Write-Host "========================================" -ForegroundColor Cyan
Write-Host ""

$ErrorActionPreference = "Continue"

switch ($Type) {
    "all" {
        Write-Host "Running all tests..." -ForegroundColor Yellow
        go test -v ./...
    }
    "unit" {
        Write-Host "Running unit tests..." -ForegroundColor Yellow
        go test -v -short ./internal/...
    }
    "integration" {
        Write-Host "Running integration tests..." -ForegroundColor Yellow
        go test -v ./tests/...
    }
    "coverage" {
        Write-Host "Running tests with coverage..." -ForegroundColor Yellow
        go test -v -coverprofile=coverage.out -covermode=atomic ./...

        if ($LASTEXITCODE -eq 0) {
            Write-Host ""
            Write-Host "Generating HTML report..." -ForegroundColor Yellow
            go tool cover -html=coverage.out -o coverage.html

            Write-Host ""
            Write-Host "Coverage summary:" -ForegroundColor Cyan
            go tool cover -func=coverage.out | Select-String "total"

            Write-Host ""
            Write-Host "[OK] Coverage report: coverage.html" -ForegroundColor Green

            $open = Read-Host "Open coverage report in browser? (y/N)"
            if ($open -eq "y" -or $open -eq "Y") {
                Start-Process coverage.html
            }
        }
    }
    "verbose" {
        Write-Host "Running tests (verbose)..." -ForegroundColor Yellow
        go test -v -count=1 ./...
    }
    "bench" {
        Write-Host "Running benchmarks..." -ForegroundColor Yellow
        go test -bench=. -benchmem ./...
    }
    default {
        Write-Host "Unknown test type: $Type" -ForegroundColor Red
        Write-Host ""
        Write-Host "Usage:" -ForegroundColor Yellow
        Write-Host "  .\test.ps1              # Run all tests"
        Write-Host "  .\test.ps1 unit         # Run unit tests"
        Write-Host "  .\test.ps1 integration  # Run integration tests"
        Write-Host "  .\test.ps1 coverage     # Run with coverage"
        Write-Host "  .\test.ps1 verbose      # Run with verbose output"
        Write-Host "  .\test.ps1 bench        # Run benchmarks"
        exit 1
    }
}

Write-Host ""
if ($LASTEXITCODE -eq 0) {
    Write-Host "========================================" -ForegroundColor Cyan
    Write-Host "  All tests passed!" -ForegroundColor Green
    Write-Host "========================================" -ForegroundColor Cyan
} else {
    Write-Host "========================================" -ForegroundColor Cyan
    Write-Host "  Some tests failed!" -ForegroundColor Red
    Write-Host "========================================" -ForegroundColor Cyan
}
Write-Host ""

exit $LASTEXITCODE