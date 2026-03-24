param(
    [string]$FuzzTime = "30s"
)

$ErrorActionPreference = "Continue"

function Section($title) {
    Write-Host ""
    Write-Host ("=" * 60) -ForegroundColor Cyan
    Write-Host "  $title" -ForegroundColor Cyan
    Write-Host ("=" * 60) -ForegroundColor Cyan
}

# ------------------------------------------------------------
# 1. All tests
# ------------------------------------------------------------
Section "1/3  go test ./..."
go test -v -count=1 ./...

# ------------------------------------------------------------
# 2. Coverage
# ------------------------------------------------------------
Section "2/3  Coverage"
$coverFile = Join-Path $PSScriptRoot "coverage.out"
go test "-coverprofile=$coverFile" ./...
Write-Host ""
go tool cover "-func=$coverFile" | Select-String "^total"

# To open HTML report uncomment the next line:
# go tool cover "-html=$coverFile"

# ------------------------------------------------------------
# 3. Fuzz seed-corpus (no random generation, just replays)
# ------------------------------------------------------------
Section "3/3  Fuzz seed-corpus"

$fuzzCases = @(
    @("./internal/api/...",       "FuzzHandleAddRule"),
    @("./internal/api/...",       "FuzzHandleBulkReplace"),
    @("./internal/api/...",       "FuzzHandleImport"),
    @("./internal/api/...",       "FuzzDeleteRuleValue"),
    @("./internal/api/...",       "FuzzCORSOrigin"),
    @("./internal/api/...",       "FuzzSetDefault"),
    @("./internal/apprules/...", "FuzzNormalizePattern"),
    @("./internal/apprules/...", "FuzzMatcher"),
    @("./internal/apprules/...", "FuzzMatcherSafety"),
    @("./internal/apprules/...", "FuzzMatchAny"),
    @("./internal/config/...",    "FuzzNormalizeRuleValue"),
    @("./internal/config/...",    "FuzzDetectRuleType"),
    @("./internal/config/...",    "FuzzParseVLESSURL"),
    @("./internal/config/...",    "FuzzRoutingRoundTrip"),
    @("./internal/config/...",    "FuzzSanitizeRoutingConfig"),
    @("./internal/wintun/...",    "FuzzStopFileContent"),
    @("./internal/wintun/...",    "FuzzGapFileContent"),
    @("./internal/wintun/...",    "FuzzMarkerSequence"),
    @("./internal/wintun/...",    "FuzzPollUntilFreeFiles"),
    @("./internal/xray/...",      "FuzzIsTunConflict"),
    @("./internal/xray/...",      "FuzzTailWriter"),
    @("./internal/xray/...",      "FuzzCrashDetection")
)

foreach ($case in $fuzzCases) {
    $pkg  = $case[0]
    $name = $case[1]
    Write-Host ""
    Write-Host "  >> $name  ($pkg)" -ForegroundColor Yellow
    go test "-run=^${name}$" -v $pkg
}

# ------------------------------------------------------------
# Active fuzzing - uncomment what you need
# ------------------------------------------------------------
# Section "Active fuzzing ($FuzzTime each)"
# go test "-fuzz=FuzzMatcher"               "-fuzztime=$FuzzTime" ./internal/apprules/...
# go test "-fuzz=FuzzNormalizeRuleValue"    "-fuzztime=$FuzzTime" ./internal/config/...
# go test "-fuzz=FuzzHandleAddRule"         "-fuzztime=$FuzzTime" ./internal/api/...
# go test "-fuzz=FuzzHandleBulkReplace"     "-fuzztime=$FuzzTime" ./internal/api/...
# go test "-fuzz=FuzzHandleImport"          "-fuzztime=$FuzzTime" ./internal/api/...
# go test "-fuzz=FuzzDeleteRuleValue"       "-fuzztime=$FuzzTime" ./internal/api/...
# go test "-fuzz=FuzzCORSOrigin"            "-fuzztime=$FuzzTime" ./internal/api/...
# go test "-fuzz=FuzzSetDefault"            "-fuzztime=$FuzzTime" ./internal/api/...
# go test "-fuzz=FuzzNormalizePattern"      "-fuzztime=$FuzzTime" ./internal/apprules/...
# go test "-fuzz=FuzzMatcherSafety"         "-fuzztime=$FuzzTime" ./internal/apprules/...
# go test "-fuzz=FuzzMatchAny"              "-fuzztime=$FuzzTime" ./internal/apprules/...
# go test "-fuzz=FuzzDetectRuleType"        "-fuzztime=$FuzzTime" ./internal/config/...
# go test "-fuzz=FuzzParseVLESSURL"         "-fuzztime=$FuzzTime" ./internal/config/...
# go test "-fuzz=FuzzRoutingRoundTrip"      "-fuzztime=$FuzzTime" ./internal/config/...
# go test "-fuzz=FuzzSanitizeRoutingConfig" "-fuzztime=$FuzzTime" ./internal/config/...
# go test "-fuzz=FuzzStopFileContent"       "-fuzztime=$FuzzTime" ./internal/wintun/...
# go test "-fuzz=FuzzGapFileContent"        "-fuzztime=$FuzzTime" ./internal/wintun/...
# go test "-fuzz=FuzzMarkerSequence"        "-fuzztime=$FuzzTime" ./internal/wintun/...
# go test "-fuzz=FuzzPollUntilFreeFiles"    "-fuzztime=$FuzzTime" ./internal/wintun/...
# go test "-fuzz=FuzzIsTunConflict"         "-fuzztime=$FuzzTime" ./internal/xray/...
# go test "-fuzz=FuzzTailWriter"            "-fuzztime=$FuzzTime" ./internal/xray/...
# go test "-fuzz=FuzzCrashDetection"        "-fuzztime=$FuzzTime" ./internal/xray/...

# ------------------------------------------------------------
# E2E integration test (requires sing-box.exe)
# ------------------------------------------------------------
# $env:SINGBOX_PATH = "C:\path\to\sing-box.exe"
# go test "-run=TestKillOrphanSingBox_Integration" -v -timeout=120s ./cmd/proxy-client/...

Section "Done"
