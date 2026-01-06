# monitor.ps1
# Real-time monitoring for proxy-client

Write-Host "Starting Proxy Client Monitor..." -ForegroundColor Cyan
Write-Host "Press Ctrl+C to stop" -ForegroundColor Gray
Write-Host ""
Start-Sleep -Seconds 2

$iteration = 0

while ($true) {
    Clear-Host
    $iteration++

    Write-Host "========================================" -ForegroundColor Cyan
    Write-Host "  Proxy Client - Live Monitor" -ForegroundColor Cyan
    Write-Host "========================================" -ForegroundColor Cyan
    Write-Host "Time: $(Get-Date -Format 'yyyy-MM-dd HH:mm:ss')" -ForegroundColor Gray
    Write-Host "Refresh: #$iteration (every 5 seconds)" -ForegroundColor Gray
    Write-Host ""

    # API Status
    Write-Host "API Status:" -ForegroundColor Yellow
    try {
        $status = Invoke-RestMethod "http://localhost:8080/api/status" -TimeoutSec 3
        Write-Host "  [ONLINE] API Server" -ForegroundColor Green
        Write-Host ""

        Write-Host "  XRay:" -ForegroundColor White
        if ($status.xray.running) {
            Write-Host "    Status: Running" -ForegroundColor Green
            Write-Host "    PID: $($status.xray.pid)" -ForegroundColor Gray
        } else {
            Write-Host "    Status: Stopped" -ForegroundColor Red
        }
        Write-Host ""

        Write-Host "  Proxy:" -ForegroundColor White
        if ($status.proxy.enabled) {
            Write-Host "    Status: Enabled" -ForegroundColor Green
            Write-Host "    Address: $($status.proxy.address)" -ForegroundColor Gray
        } else {
            Write-Host "    Status: Disabled" -ForegroundColor Yellow
        }
        Write-Host ""

        Write-Host "  Config: $($status.config_path)" -ForegroundColor Gray

    } catch {
        Write-Host "  [OFFLINE] API Server" -ForegroundColor Red
        Write-Host "  Error: $_" -ForegroundColor Red
    }
    Write-Host ""

    # Process Info
    Write-Host "Processes:" -ForegroundColor Yellow

    $xrayProc = Get-Process xray -ErrorAction SilentlyContinue
    if ($xrayProc) {
        Write-Host "  [RUNNING] XRay" -ForegroundColor Green
        Write-Host "    PID: $($xrayProc.Id)" -ForegroundColor Gray
        Write-Host "    CPU: $([math]::Round($xrayProc.CPU, 2))s" -ForegroundColor Gray
        Write-Host "    Memory: $([math]::Round($xrayProc.WorkingSet64 / 1MB, 2)) MB" -ForegroundColor Gray
    } else {
        Write-Host "  [STOPPED] XRay" -ForegroundColor Red
    }
    Write-Host ""

    $proxyProc = Get-Process proxy-client -ErrorAction SilentlyContinue
    if ($proxyProc) {
        Write-Host "  [RUNNING] Proxy Client" -ForegroundColor Green
        Write-Host "    PID: $($proxyProc.Id)" -ForegroundColor Gray
        Write-Host "    CPU: $([math]::Round($proxyProc.CPU, 2))s" -ForegroundColor Gray
        Write-Host "    Memory: $([math]::Round($proxyProc.WorkingSet64 / 1MB, 2)) MB" -ForegroundColor Gray
    } else {
        Write-Host "  [STOPPED] Proxy Client" -ForegroundColor Red
    }
    Write-Host ""

    # Network Ports
    Write-Host "Network Ports:" -ForegroundColor Yellow

    $apiListening = Test-NetConnection -ComputerName 127.0.0.1 -Port 8080 -WarningAction SilentlyContinue -InformationLevel Quiet
    if ($apiListening) {
        Write-Host "  [LISTENING] Port 8080 (API)" -ForegroundColor Green
    } else {
        Write-Host "  [CLOSED] Port 8080 (API)" -ForegroundColor Red
    }

    $proxyListening = Test-NetConnection -ComputerName 127.0.0.1 -Port 10807 -WarningAction SilentlyContinue -InformationLevel Quiet
    if ($proxyListening) {
        Write-Host "  [LISTENING] Port 10807 (Proxy)" -ForegroundColor Green
    } else {
        Write-Host "  [CLOSED] Port 10807 (Proxy)" -ForegroundColor Red
    }
    Write-Host ""

    # Windows Proxy
    Write-Host "Windows Proxy:" -ForegroundColor Yellow
    try {
        $winProxy = Get-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings"
        if ($winProxy.ProxyEnable -eq 1) {
            Write-Host "  [ENABLED] System Proxy" -ForegroundColor Green
            Write-Host "    Server: $($winProxy.ProxyServer)" -ForegroundColor Gray
            Write-Host "    Override: $($winProxy.ProxyOverride)" -ForegroundColor Gray
        } else {
            Write-Host "  [DISABLED] System Proxy" -ForegroundColor Yellow
        }
    } catch {
        Write-Host "  [ERROR] Cannot read registry" -ForegroundColor Red
    }
    Write-Host ""

    # Files
    Write-Host "Configuration Files:" -ForegroundColor Yellow
    $files = @(
        @{Path="config.template.json"; Name="Template"},
        @{Path="secret.key"; Name="Secret Key"},
        @{Path="config.runtime.json"; Name="Runtime"},
        @{Path="xray_core/xray.exe"; Name="XRay Core"}
    )

    foreach ($file in $files) {
        if (Test-Path $file.Path) {
            Write-Host "  [OK] $($file.Name)" -ForegroundColor Green
        } else {
            Write-Host "  [MISSING] $($file.Name)" -ForegroundColor Red
        }
    }
    Write-Host ""

    Write-Host "========================================" -ForegroundColor Cyan
    Write-Host "Next refresh in 5 seconds..." -ForegroundColor Gray

    Start-Sleep -Seconds 5
}