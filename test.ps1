<#
.SYNOPSIS
    Тестовый раннер proxy-client.

.EXAMPLE
    .\test.ps1                        # unit tests + race detector
    .\test.ps1 -Type coverage
    .\test.ps1 -Type fuzz -FuzzTime 60
    .\test.ps1 -Type all              # race -> coverage -> fuzz за один прогон
    .\test.ps1 -Type all -FuzzTime 60
#>
param(
    [string]$Type      = "race",
    [int]   $FuzzTime  = 30,
    [int]   $BatchSize = 6          # fuzz-задачи запускаются батчами (защита от resource exhaustion)
)

$ErrorActionPreference = "Continue"
$script:failed = $false

# ── Лог-файл ──────────────────────────────────────────────────────────────────
$LogDir = Join-Path $PSScriptRoot "test-output"
if (-not (Test-Path $LogDir)) { New-Item -ItemType Directory -Force -Path $LogDir | Out-Null }
$stamp   = Get-Date -Format "yyyyMMdd_HHmmss"
$LogFile = Join-Path $LogDir "${Type}_${stamp}.txt"

function Write-Log([string]$msg) {
    $msg | Out-File -FilePath $LogFile -Append -Encoding UTF8
}

function Write-Both([string]$msg, [string]$color = "White") {
    Write-Host $msg -ForegroundColor $color
    Write-Log $msg
}

# В консоль — только строки с FAIL (красным). Всё остальное только в лог.
function Run-Go([string]$desc, [string[]]$goArgs) {
    Write-Both ">> $desc" Yellow
    $output   = & go $goArgs 2>&1
    $exitCode = $LASTEXITCODE
    foreach ($line in $output) {
        Write-Log "$line"
        if ("$line" -match "FAIL") {
            Write-Host "  $line" -ForegroundColor Red
        }
    }
    if ($exitCode -ne 0) { $script:failed = $true }
}

# ── Блоки ─────────────────────────────────────────────────────────────────────
function Run-Race {
    Write-Both "=== Unit tests + race detector ===" Cyan
    $script:failed = $false
    Run-Go "Tests" @("test", "-race", "-timeout=120s", "./cmd/...", "./internal/...")
    if ($script:failed) {
        Write-Both "FAILED  (подробности: $LogFile)" Red
        return $false
    }
    Write-Both "PASSED" Green
    return $true
}

function Run-Coverage {
    Write-Both "=== Coverage ===" Cyan
    $script:failed = $false
    Run-Go "Coverage" @("test", "-coverprofile=coverage.out", "./cmd/...", "./internal/...")
    if (-not $script:failed) {
        & go tool cover -html=coverage.out -o coverage.html
        Write-Both "Отчёт: coverage.html" DarkGray
    }
    return (-not $script:failed)
}

function Run-Fuzz([int]$fuzzSec, [int]$batchSz) {
    Write-Both "=== Fuzz tests ($fuzzSec s, batch=$batchSz) ===" Cyan

    $script:failed = $false
    Run-Go "Pre-check" @("test", "-race", "-timeout=120s", "./cmd/...", "./internal/...")
    if ($script:failed) {
        Write-Both "Unit tests failed, skipping fuzz." Red
        return $false
    }

    # Каждая строка: "пакет|ИмяФазз"
    # ВАЖНО: имена должны быть точными — в go test передаётся как regexp ^Name$,
    # иначе FuzzMatcher матчит FuzzMatcherSafety и весь пакет падает с ошибкой
    # "will not fuzz, -fuzz matches more than one fuzz test".
    $rawTargets = @(
        "./internal/config/|FuzzNormalizeRuleValue",
        "./internal/config/|FuzzDetectRuleType",
        "./internal/config/|FuzzParseVLESSURL",
        "./internal/config/|FuzzRoutingRoundTrip",
        "./internal/config/|FuzzSanitizeRoutingConfig",
        "./internal/apprules/|FuzzNormalizePattern",
        "./internal/apprules/|FuzzMatcher",
        "./internal/apprules/|FuzzMatcherSafety",
        "./internal/apprules/|FuzzMatchAny",
        "./internal/xray/|FuzzIsTunConflict",
        "./internal/xray/|FuzzTailWriter",
        "./internal/xray/|FuzzCrashDetection",
        "./internal/wintun/|FuzzStopFileContent",
        "./internal/wintun/|FuzzGapFileContent",
        "./internal/wintun/|FuzzMarkerSequence",
        "./internal/wintun/|FuzzPollUntilFreeFiles",
        "./internal/api/|FuzzHandleAddRule",
        "./internal/api/|FuzzHandleBulkReplace",
        "./internal/api/|FuzzHandleImport",
        "./internal/api/|FuzzDeleteRuleValue",
        "./internal/api/|FuzzCORSOrigin",
        "./internal/api/|FuzzSetDefault"
    )

    $root        = $PWD.Path
    $fuzzFailed  = $false
    # Таймаут на один job: fuzzSec + 3 минуты на overhead (baseline coverage, минимизация)
    $jobTimeout  = $fuzzSec + 180

    # Разбиваем на батчи, чтобы не запускать 22 процесса одновременно —
    # при параллельном запуске wintun/api тесты вызывают netsh/exec и исчерпывают ресурсы.
    $batches = @()
    $batch   = @()
    foreach ($line in $rawTargets) {
        $batch += $line
        if ($batch.Count -ge $batchSz) {
            $batches += ,@($batch)
            $batch = @()
        }
    }
    if ($batch.Count -gt 0) { $batches += ,@($batch) }

    $batchNum = 0
    foreach ($batchItems in $batches) {
        $batchNum++
        Write-Both "  Batch $batchNum/$($batches.Count) [$($batchItems.Count) targets]..." DarkGray

        $jobs = @()
        foreach ($line in $batchItems) {
            $parts = $line.Split("|")
            $p = $parts[0]
            $n = $parts[1]
            Write-Host "    Starting $n..." -ForegroundColor DarkGray

            $jobs += Start-Job -ScriptBlock {
                param($r, $pkg, $name, $fuzzSec, $jobTimeout)
                Set-Location $r
                # Точный regexp: ^FuzzMatcher$ не матчит FuzzMatcherSafety
                $pattern = "^" + $name + '$'
                $o = & go test -run=NONE "-fuzz=$pattern" "-fuzztime=${fuzzSec}s" "-timeout=${jobTimeout}s" $pkg 2>&1
                return [PSCustomObject]@{ Name = $name; Out = ($o -join "`n"); Code = $LASTEXITCODE }
            } -ArgumentList $root, $p, $n, $fuzzSec, $jobTimeout
        }

        $results = $jobs | Wait-Job | Receive-Job
        $jobs | Remove-Job -Force

        foreach ($r in $results) {
            Write-Log "--- $($r.Name) ---"
            Write-Log $r.Out
            if ($r.Code -ne 0) {
                Write-Host "  [FAIL] $($r.Name)" -ForegroundColor Red
                $fuzzFailed = $true
            }
        }
    }

    if ($fuzzFailed) {
        Write-Both "Fuzzing found issues!  (подробности: $LogFile)" Red
        return $false
    }
    Write-Both "Fuzzing passed." Green
    return $true
}

# ── Точка входа ───────────────────────────────────────────────────────────────
switch ($Type) {
    "race" {
        $ok = Run-Race
        exit ([int](-not $ok))
    }
    "coverage" {
        $ok = Run-Coverage
        exit ([int](-not $ok))
    }
    "fuzz" {
        $ok = Run-Fuzz $FuzzTime $BatchSize
        exit ([int](-not $ok))
    }
    "all" {
        Write-Both "=== ALL: race -> coverage -> fuzz ($FuzzTime s, batch=$BatchSize) ===" Magenta

        $ok = Run-Race
        if (-not $ok) { Write-Both "ALL: остановлено на race." Red; exit 1 }

        $ok = Run-Coverage
        if (-not $ok) { Write-Both "ALL: остановлено на coverage." Red; exit 1 }

        $ok = Run-Fuzz $FuzzTime $BatchSize
        if (-not $ok) { Write-Both "ALL: fuzz нашёл проблемы." Red; exit 1 }

        Write-Both "ALL: все этапы прошли успешно." Green
        exit 0
    }
    default {
        Write-Host "Usage: .\test.ps1 [-Type race|coverage|fuzz|all] [-FuzzTime <sec>] [-BatchSize <n>]" -ForegroundColor Yellow
        exit 1
    }
}
