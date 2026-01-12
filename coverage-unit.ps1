# coverage-unit.ps1
# Generate test coverage report for unit tests only

Write-Host ""
Write-Host "========================================" -ForegroundColor Cyan
Write-Host "  Unit Test Coverage Report" -ForegroundColor Cyan
Write-Host "========================================" -ForegroundColor Cyan
Write-Host ""

Write-Host "Running unit tests with coverage..." -ForegroundColor Yellow
Write-Host ""

# Run only unit tests (skip integration)
go test -short -coverprofile coverage.out -covermode atomic ./...

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
Write-Host ""

# Show detailed coverage by package
go tool cover -func coverage.out | Select-String -Pattern "internal/"

Write-Host ""
Write-Host "Total Coverage:" -ForegroundColor Cyan
go tool cover -func coverage.out | Select-String "total"

Write-Host ""
Write-Host "[OK] Coverage report generated!" -ForegroundColor Green
Write-Host "  File: coverage.html" -ForegroundColor Gray
Write-Host ""

# Calculate average
$coverageLines = go tool cover -func coverage.out | Select-String -Pattern "internal/"
$totalCoverage = 0
$count = 0

foreach ($line in $coverageLines) {
    if ($line -match "(\d+\.\d+)%") {
        $totalCoverage += [double]$matches[1]
        $count++
    }
}

if ($count -gt 0) {
    $avgCoverage = [math]::Round($totalCoverage / $count, 1)
    Write-Host "Average package coverage: $avgCoverage%" -ForegroundColor Cyan
    Write-Host ""
}

$open = Read-Host "Open coverage report in browser? (y/N)"
if ($open -eq "y" -or $open -eq "Y") {
    Write-Host "Opening coverage.html..." -ForegroundColor Yellow
    Start-Process coverage.html
}

Write-Host ""