# Release Process

This document is the developer checklist. The automation details are refined in
prompt 27.

## Pre-Release Checks

1. Update `CHANGELOG.md`.
2. Run formatting and static checks.
3. Run full tests:

```powershell
go build ./...
$env:GOOS='linux'; $env:GOARCH='amd64'; go build ./...
Remove-Item Env:GOOS -ErrorAction SilentlyContinue
Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
go test ./... -race -count=1 -timeout=300s
```

4. Build release artifacts:

```powershell
.\build.ps1 -Release
```

5. Verify `proxy-client.exe` starts and shows the expected version.
6. Verify `sing-box check -c config.singbox.json` for generated configs used in
   release tests.

## Release Metadata

Release metadata must include:

- version
- channel
- download URL
- SHA256
- size in bytes
- release notes URL

The updater refuses mismatched channel, size, or checksum.

## Rollback

Keep the previous release artifact and metadata available until the new release
is verified. Do not overwrite published artifacts in place.

## CI Gate

Release is blocked if any required GitHub Actions job is red. Fix the job or the
code; do not remove the job.
