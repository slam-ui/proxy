# coverage.ps1
# Generate test coverage report

Write-Host ""
Write-Host "========================================" -ForegroundColor Cyan
Write-Host "  Test Coverage Report" -ForegroundColor Cyan
Write-Host "========================================" -ForegroundColor Cyan
Write-Host ""

Write-Host "Running tests with coverage..." -ForegroundColor Yellow
Write-Host ""

# Run tests with coverage
go test -coverprofile coverage.out -covermode atomic ./...

if ($LASTEXITCODE -ne 0) {
    Write-Host ""
    Write-Host "[ERROR] Tests failed!" -ForegroundColor Red
    exit 1
}

Write-Host ""
Write-Host "Generating HTML report..." -ForegroundColor Yellow
go tool cover -html coverage.out -o coverage.html

Write-Host ""
Write-Host "========================================" -ForegroundColor Cyan
Write-Host "Coverage Summary:" -ForegroundColor Yellow
Write-Host "========================================" -ForegroundColor Cyan
go tool cover -func coverage.out | Select-String "total"

Write-Host ""
Write-Host "[OK] Coverage report generated!" -ForegroundColor Green
Write-Host "  File: coverage.html" -ForegroundColor Gray
Write-Host ""

$open = Read-Host "Open coverage report in browser? (y/N)"
if ($open -eq "y" -or $open -eq "Y") {
    Write-Host "Opening coverage.html..." -ForegroundColor Yellow
    Start-Process coverage.html
}

Write-Host ""
Write-Host "You can also view coverage with:" -ForegroundColor Cyan
Write-Host "  go tool cover -func coverage.out        # Summary by function" -ForegroundColor White
Write-Host "  go tool cover -html coverage.out        # Interactive HTML" -ForegroundColor White
Write-Host ""