# Release Process

SafeSky releases are tag-driven. A pushed `v*` tag builds the Windows app,
updater, MSI installer, portable ZIP, SHA256 manifest, and `version.<channel>.json`
metadata in GitHub Actions.

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

4. Build release artifacts locally if you need to inspect them before tagging:

```powershell
.\build\package-release.ps1 -Version v0.9.4
```

5. Verify `SafeSky.exe` starts and shows the expected version, commit, and build
   time in Settings.
6. Verify `sing-box check -c config.singbox.json` for generated configs used in
   release tests.
7. Manual smoke test on a clean Windows 10/11 VM:
   - Install MSI.
   - Complete first-run setup.
   - Connect.
   - Disconnect.
   - Update from the previous version if one exists.
   - Uninstall and confirm binaries, shortcuts, and registry entries are removed.

## Release Metadata

Release metadata must include:

- version
- channel
- download URL
- SHA256
- size in bytes
- release notes URL

The updater refuses mismatched channel, size, or checksum.

Channels:

- `stable`: normal `vMAJOR.MINOR.PATCH` tags.
- `beta`: prerelease tags such as `v0.9.4-beta1`.
- `dev`: reserved for internal metadata and not published by the tag workflow.

The metadata `download_url` points at the raw `SafeSky-<version>-windows-amd64.exe`
asset because the current self-updater replaces the executable in place. The MSI
and portable ZIP are published for human installs.

## Tag Release

```powershell
git tag v0.9.4
git push origin v0.9.4
```

The release workflow creates the GitHub Release and uploads:

- `SafeSky-X.Y.Z-windows-amd64.exe`
- `proxy-client-X.Y.Z-windows-amd64.msi`
- `proxy-client-X.Y.Z-windows-amd64.zip`
- `proxy-updater-X.Y.Z-windows-amd64.exe`
- `version.stable.json` or `version.beta.json`
- `SHA256SUMS.txt`
- `CHANGELOG.md`

## Rollback

Keep the previous release artifact and metadata available until the new release
is verified. Do not overwrite published artifacts in place.

Emergency rollback:

1. Re-publish metadata for the previous version.
2. Set `force_downgrade: true` only for the emergency rollback window.
3. Publish a fixed patch release and return `force_downgrade` to `false`.

## CI Gate

Release is blocked if any required GitHub Actions job is red. Fix the job or the
code; do not remove the job.
