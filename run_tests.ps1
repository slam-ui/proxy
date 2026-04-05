<#
.SYNOPSIS
    Быстрый запуск тестов proxy-client.

.EXAMPLE
    .\run_tests.ps1              # unit tests + race detector
    .\run_tests.ps1 coverage     # тесты + HTML отчёт покрытия
    .\run_tests.ps1 fuzz 60      # fuzz-тесты (60 с каждый)
    .\run_tests.ps1 all          # race → coverage → fuzz за один прогон
    .\run_tests.ps1 all 60       # то же, но fuzz 60 с
#>
param(
    [string]$Mode     = "race",
    [int]   $FuzzTime = 30
)

$ErrorActionPreference = "Stop"
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path

$testScript = Join-Path $ScriptDir "test.ps1"
if (-not (Test-Path $testScript)) {
    Write-Host "[ERROR] test.ps1 не найден: $testScript" -ForegroundColor Red
    exit 1
}

$go = Get-Command go -ErrorAction SilentlyContinue
if (-not $go) {
    Write-Host "[ERROR] go не найден в PATH" -ForegroundColor Red
    exit 1
}
Write-Host "  $(& go version)" -ForegroundColor DarkGray
Write-Host ""

& $testScript -Type $Mode -FuzzTime $FuzzTime
exit $LASTEXITCODE
