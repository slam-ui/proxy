# test-proxy.ps1
# Automatic integration test for proxy-client

Write-Host ""
Write-Host "========================================" -ForegroundColor Cyan
Write-Host "  Proxy Client - Integration Test" -ForegroundColor Cyan
Write-Host "========================================" -ForegroundColor Cyan
Write-Host ""

$failed = 0
$passed = 0

function Test-Result {
    param($condition, $testName)

    if ($condition) {
        Write-Host "  [PASS] $testName" -ForegroundColor Green
        $script:passed++
        return $true
    } else {
        Write-Host "  [FAIL] $testName" -ForegroundColor Red
        $script:failed++
        return $false
    }
}

# Test 1: API Health Check
Write-Host "Test 1: API Health Check" -ForegroundColor Yellow
try {
    $response = Invoke-RestMethod -Uri "http://localhost:8080/api/health" -TimeoutSec 5 -ErrorAction Stop
    Test-Result ($response.status -eq "ok") "API responds with OK"
} catch {
    Test-Result $false "API Health (Error: $_)"
}
Write-Host ""

# Test 2: API Status
Write-Host "Test 2: API Status Check" -ForegroundColor Yellow
try {
    $status = Invoke-RestMethod -Uri "http://localhost:8080/api/status" -TimeoutSec 5 -ErrorAction Stop

    Test-Result ($status.xray.running) "XRay is running"
    if ($status.xray.running) {
        Write-Host "       PID: $($status.xray.pid)" -ForegroundColor Gray
    }

    Test-Result ($status.proxy.enabled) "Proxy is enabled"
    if ($status.proxy.enabled) {
        Write-Host "       Address: $($status.proxy.address)" -ForegroundColor Gray
    }

    Test-Result ($status.config_path -ne "") "Config path is set"
} catch {
    Test-Result $false "API Status (Error: $_)"
}
Write-Host ""

# Test 3: Process Check
Write-Host "Test 3: Process Check" -ForegroundColor Yellow
$xrayProcess = Get-Process xray -ErrorAction SilentlyContinue
if (Test-Result ($null -ne $xrayProcess) "XRay process is running") {
    Write-Host "       PID: $($xrayProcess.Id), CPU: $($xrayProcess.CPU)" -ForegroundColor Gray
}

$proxyProcess = Get-Process proxy-client -ErrorAction SilentlyContinue
if (Test-Result ($null -ne $proxyProcess) "Proxy-client process is running") {
    Write-Host "       PID: $($proxyProcess.Id), CPU: $($proxyProcess.CPU)" -ForegroundColor Gray
}
Write-Host ""

# Test 4: Windows Registry
Write-Host "Test 4: Windows Proxy Settings" -ForegroundColor Yellow
try {
    $proxy = Get-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings"

    Test-Result ($proxy.ProxyEnable -eq 1) "Proxy enabled in registry"
    Test-Result ($proxy.ProxyServer -eq "127.0.0.1:10807") "Proxy server is correct"

    if ($proxy.ProxyEnable -eq 1) {
        Write-Host "       ProxyServer: $($proxy.ProxyServer)" -ForegroundColor Gray
        Write-Host "       ProxyOverride: $($proxy.ProxyOverride)" -ForegroundColor Gray
    }
} catch {
    Test-Result $false "Registry check (Error: $_)"
}
Write-Host ""

# Test 5: Port Listening
Write-Host "Test 5: Network Port Check" -ForegroundColor Yellow
$apiPort = Test-NetConnection -ComputerName 127.0.0.1 -Port 8080 -WarningAction SilentlyContinue -InformationLevel Quiet
Test-Result $apiPort "API port 8080 is listening"

$proxyPort = Test-NetConnection -ComputerName 127.0.0.1 -Port 10807 -WarningAction SilentlyContinue -InformationLevel Quiet
Test-Result $proxyPort "Proxy port 10807 is listening"
Write-Host ""

# Test 6: Configuration Files
Write-Host "Test 6: Configuration Files" -ForegroundColor Yellow
Test-Result (Test-Path "config.template.json") "config.template.json exists"
Test-Result (Test-Path "secret.key") "secret.key exists"
Test-Result (Test-Path "config.runtime.json") "config.runtime.json exists"
Test-Result (Test-Path "xray_core/xray.exe") "xray.exe exists"
Write-Host ""

# Test 7: Config Validation
Write-Host "Test 7: Configuration Validation" -ForegroundColor Yellow
if (Test-Path "config.runtime.json") {
    try {
        $runtimeConfig = Get-Content "config.runtime.json" -Raw | ConvertFrom-Json

        Test-Result ($runtimeConfig.inbounds.Count -gt 0) "Inbounds configured"
        Test-Result ($runtimeConfig.outbounds.Count -gt 0) "Outbounds configured"

        $vnext = $runtimeConfig.outbounds[0].settings.vnext[0]
        Test-Result ($vnext.address -ne "YOUR_SERVER_ADDRESS") "Server address is configured"
        Test-Result ($vnext.users[0].id -ne "YOUR_UUID") "UUID is configured"
    } catch {
        Test-Result $false "Config validation (Error: $_)"
    }
}
Write-Host ""

# Test 8: API Operations
Write-Host "Test 8: API Operations" -ForegroundColor Yellow
try {
    # Test disable
    $disableResponse = Invoke-RestMethod -Uri "http://localhost:8080/api/proxy/disable" -Method Post -TimeoutSec 5
    Start-Sleep -Seconds 1

    $proxyAfterDisable = Get-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings"
    Test-Result ($proxyAfterDisable.ProxyEnable -eq 0) "Proxy disable works"

    # Test enable
    $enableResponse = Invoke-RestMethod -Uri "http://localhost:8080/api/proxy/enable" -Method Post -TimeoutSec 5
    Start-Sleep -Seconds 1

    $proxyAfterEnable = Get-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings"
    Test-Result ($proxyAfterEnable.ProxyEnable -eq 1) "Proxy enable works"
} catch {
    Test-Result $false "API operations (Error: $_)"
}
Write-Host ""

# Summary
Write-Host "========================================" -ForegroundColor Cyan
$total = $passed + $failed
$passRate = if ($total -gt 0) { [math]::Round(($passed / $total) * 100, 1) } else { 0 }

if ($failed -eq 0) {
    Write-Host "ALL TESTS PASSED! ($passed/$total)" -ForegroundColor Green
    Write-Host "Pass Rate: 100%" -ForegroundColor Green
} else {
    Write-Host "Tests: $passed passed, $failed failed" -ForegroundColor Yellow
    Write-Host "Pass Rate: $passRate%" -ForegroundColor Yellow
}
Write-Host "========================================" -ForegroundColor Cyan
Write-Host ""

if ($failed -eq 0) {
    Write-Host "Your proxy-client is working correctly!" -ForegroundColor Green
    Write-Host ""
    Write-Host "Next steps:" -ForegroundColor Cyan
    Write-Host "  - Test browsing through proxy" -ForegroundColor White
    Write-Host "  - Check your external IP: curl https://ifconfig.me" -ForegroundColor White
    Write-Host "  - Monitor with: .\monitor.ps1" -ForegroundColor White
} else {
    Write-Host "Some tests failed. Check the output above." -ForegroundColor Red
    Write-Host ""
    Write-Host "Common issues:" -ForegroundColor Cyan
    Write-Host "  - Make sure proxy-client.exe is running" -ForegroundColor White
    Write-Host "  - Check secret.key has valid VLESS URL" -ForegroundColor White
    Write-Host "  - Verify xray.exe is in xray_core/" -ForegroundColor White
}
Write-Host ""

exit $failed