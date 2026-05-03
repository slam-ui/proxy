# Building SafeSky

## Requirements

- Windows 10/11 x64.
- Go `1.24` with toolchain `go1.26.2`.
- PowerShell.
- WebView2 Runtime.
- `goversioninfo` for release resources.

## Common Commands

```powershell
go build ./...
$env:GOOS='linux'; $env:GOARCH='amd64'; go build ./...
Remove-Item Env:GOOS -ErrorAction SilentlyContinue
Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
go test ./... -race -count=1 -timeout=300s
```

## App Build

```powershell
.\build.ps1
.\build.ps1 -Release
.\build.ps1 -SkipTests
```

The build output is `dist/`. Do not commit generated binaries, logs, runtime
state, or local secrets from `dist/`.

## Runtime Dependencies

Packaged builds need these files next to `proxy-client.exe`:

- `sing-box.exe`
- `wintun.dll`

If `sing-box.exe` is missing, SafeSky can download and verify it at runtime.

## Platform Notes

Windows-only files must use `//go:build windows` and have non-Windows stubs where
the package is imported cross-platform. Always run both Windows and Linux builds
after touching platform code.
