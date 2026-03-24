# test.ps1 - proxy-client test runner
#
# Usage:
#   .\test.ps1              # Unit tests + race detector
#   .\test.ps1 race         # Same
#   .\test.ps1 fuzz         # All 22 fuzz tests, 30s each (parallel)
#   .\test.ps1 fuzz 60      # Fuzz tests, 60s each
#   .\test.ps1 coverage     # Tests + HTML coverage report
#   .\test.ps1 bench        # Benchmarks

param(
    [string]$Type = "race",
    [int]$FuzzTime = 30
)

$ErrorActionPreference = "Continue"
$failed = $false

function Write-Header([string]$title) {
    Write-Host ""
    Write-Host "========================================" -ForegroundColor Cyan
    Write-Host "  $title" -ForegroundColor Cyan
    Write-Host "========================================" -ForegroundColor Cyan
    Write-Host ""
}

function Write-Footer {
    Write-Host ""
    if (-not $script:failed) {
        Write-Host "========================================" -ForegroundColor Cyan
        Write-Host "  All tests passed!" -ForegroundColor Green
        Write-Host "========================================" -ForegroundColor Cyan
    } else {
        Write-Host "========================================" -ForegroundColor Cyan
        Write-Host "  Some tests FAILED!" -ForegroundColor Red
        Write-Host "========================================" -ForegroundColor Cyan
    }
    Write-Host ""
}

function Run-Go([string]$desc, [string[]]$goArgs) {
    Write-Host ">> $desc" -ForegroundColor Yellow
    & go $goArgs
    if ($LASTEXITCODE -ne 0) {
        Write-Host "[FAIL] $desc" -ForegroundColor Red
        $script:failed = $true
    } else {
        Write-Host "[OK]   $desc" -ForegroundColor Green
    }
    Write-Host ""
}

# ---- race -------------------------------------------------------------------
if ($Type -eq "race" -or $Type -eq "all") {
    Write-Header "Unit tests + race detector"
    Run-Go "go test -race -timeout=120s ./..." @("test", "-race", "-timeout=120s", "./...")
    Write-Footer
    if ($failed) { exit 1 } else { exit 0 }
}

# ---- fuzz -------------------------------------------------------------------
if ($Type -eq "fuzz") {
    Write-Header "Fuzz tests ($FuzzTime s each)"

    Write-Host "Step 1: unit tests..." -ForegroundColor Yellow
    Run-Go "go test -race -timeout=120s ./..." @("test", "-race", "-timeout=120s", "./...")
    if ($failed) {
        Write-Host "Unit tests failed - skipping fuzz." -ForegroundColor Red
        exit 1
    }

    Write-Host "Step 2: starting 22 fuzz tests in parallel..." -ForegroundColor Yellow
    Write-Host ""

    $fuzzTargets = @(
        ,@("./internal/config/",   "FuzzNormalizeRuleValue")
        ,@("./internal/config/",   "FuzzDetectRuleType")
        ,@("./internal/config/",   "FuzzParseVLESSURL")
        ,@("./internal/config/",   "FuzzRoutingRoundTrip")
        ,@("./internal/config/",   "FuzzSanitizeRoutingConfig")
        ,@("./internal/apprules/", "FuzzNormalizePattern")
        ,@("./internal/apprules/", "FuzzMatcher`$")
        ,@("./internal/apprules/", "FuzzMatcherSafety")
        ,@("./internal/apprules/", "FuzzMatchAny")
        ,@("./internal/xray/",     "FuzzIsTunConflict")
        ,@("./internal/xray/",     "FuzzTailWriter")
        ,@("./internal/xray/",     "FuzzCrashDetection")
        ,@("./internal/wintun/",   "FuzzStopFileContent")
        ,@("./internal/wintun/",   "FuzzGapFileContent")
        ,@("./internal/wintun/",   "FuzzMarkerSequence")
        ,@("./internal/wintun/",   "FuzzPollUntilFreeFiles")
        ,@("./internal/api/",      "FuzzHandleAddRule")
        ,@("./internal/api/",      "FuzzHandleBulkReplace")
        ,@("./internal/api/",      "FuzzHandleImport")
        ,@("./internal/api/",      "FuzzDeleteRuleValue")
        ,@("./internal/api/",      "FuzzCORSOrigin")
        ,@("./internal/api/",      "FuzzSetDefault")
    )

    $rootDir = $PWD.Path
    $timeoutSec = $FuzzTime + 60
    $jobs = @()
    foreach ($t in $fuzzTargets) {
        $pkg  = $t[0]
        $name = $t[1]
        Write-Host "  Start: $name  ($pkg)" -ForegroundColor Gray
        $jobs += Start-Job -ScriptBlock {
            param($root, $p, $n, $ft)
            Set-Location $root
            $out = & go test "-run=^$" "-fuzz=$n" "-fuzztime=${ft}s" $p 2>&1
            [PSCustomObject]@{
                Package  = $p
                Name     = $n
                Output   = ($out -join "`n")
                ExitCode = $LASTEXITCODE
            }
        } -ArgumentList $rootDir, $pkg, $name, $FuzzTime
    }

    Write-Host ""
    Write-Host "Waiting for $($jobs.Count) jobs (timeout: $timeoutSec s)..." -ForegroundColor Yellow
    $null = $jobs | Wait-Job -Timeout $timeoutSec
    $results = $jobs | Receive-Job
    $jobs | Remove-Job -Force

    Write-Host ""
    Write-Host "--- Fuzz results ---" -ForegroundColor Cyan
    $anyFail = $false
    foreach ($r in $results) {
        if ($r.ExitCode -eq 0) {
            Write-Host "  [OK]   $($r.Name)  ($($r.Package))" -ForegroundColor Green
        } else {
            Write-Host "  [FAIL] $($r.Name)  ($($r.Package))" -ForegroundColor Red
            Write-Host $r.Output -ForegroundColor DarkRed
            $anyFail = $true
            $failed = $true
        }
    }
    Write-Host ""
    if ($anyFail) {
        Write-Host "Fuzzer found bugs! Corpus in testdata/fuzz/<name>/" -ForegroundColor Red
    } else {
        Write-Host "All fuzz tests passed with no new crash cases." -ForegroundColor Green
    }

    Write-Footer
    if ($failed) { exit 1 } else { exit 0 }
}

# ---- coverage ---------------------------------------------------------------
if ($Type -eq "coverage") {
    Write-Header "Tests + Coverage"
    Run-Go "go test -race -coverprofile=coverage.out ./..." @(
        "test", "-race", "-timeout=120s",
        "-coverprofile=coverage.out", "-covermode=atomic", "./..."
    )
    if (-not $failed) {
        $coverOut  = Join-Path $PWD "coverage.out"
        $coverHtml = Join-Path $PWD "coverage.html"
        & go tool cover -html $coverOut -o $coverHtml
        Write-Host "Coverage summary:" -ForegroundColor Cyan
        & go tool cover -func $coverOut | Select-String "total"
        Write-Host ""
        if (Test-Path $coverHtml) {
            Write-Host "[OK] Report: coverage.html" -ForegroundColor Green
            $open = Read-Host "Open in browser? (y/N)"
            if ($open -eq "y" -or $open -eq "Y") { Invoke-Item $coverHtml }
        } else {
            Write-Host "[WARN] coverage.html was not created" -ForegroundColor Yellow
        }
    }
    Write-Footer
    if ($failed) { exit 1 } else { exit 0 }
}

# ---- bench ------------------------------------------------------------------
if ($Type -eq "bench") {
    Write-Header "Benchmarks"
    Run-Go "go test -bench=. -benchmem ./..." @("test", "-bench=.", "-benchmem", "./...")
    Write-Footer
    if ($failed) { exit 1 } else { exit 0 }
}

# ---- unknown ----------------------------------------------------------------
Write-Host "Unknown mode: $Type" -ForegroundColor Red
Write-Host ""
Write-Host "Usage:" -ForegroundColor Yellow
Write-Host "  .\test.ps1              # Unit tests + race"
Write-Host "  .\test.ps1 race         # Same"
Write-Host "  .\test.ps1 fuzz         # All fuzz tests, 30s each"
Write-Host "  .\test.ps1 fuzz 60      # Fuzz tests, 60s each"
Write-Host "  .\test.ps1 coverage     # Tests + HTML coverage"
Write-Host "  .\test.ps1 bench        # Benchmarks"
exit 1
