# SafeSky Update Release Process

## Server Files

Publish one metadata file per channel:

- `version.stable.json`
- `version.beta.json`

Example:

```json
{
  "channel": "stable",
  "version": "0.9.4",
  "released_at": "2026-05-01T12:00:00Z",
  "min_version_for_direct_update": "0.7.0",
  "download_url": "https://updates.example.com/SafeSky-0.9.4-windows-amd64.exe",
  "sha256": "64-character lowercase sha256",
  "size_bytes": 24673280,
  "changelog_url": "https://updates.example.com/changelog/0.9.4.md",
  "critical": false
}
```

Both `version.<channel>.json` and `download_url` must be served over HTTPS. The client rejects HTTP and releases without SHA256.

## Build

```powershell
.\build.ps1 -Release -Version 0.9.4 -NoPause
```

The build writes:

- `dist/SafeSky.exe`
- `dist/proxy-updater.exe`
- `dist/sing-box.exe`
- `dist/data/`

`SafeSky.exe` embeds:

- `proxyclient/internal/version.Version`
- `proxyclient/internal/version.Commit`
- `proxyclient/internal/version.BuildTime`

## Publish

1. Create and push the release tag:

   ```powershell
   git tag v0.9.4
   git push origin v0.9.4
   ```

2. Build release artifacts.
3. Upload `SafeSky.exe` and `proxy-updater.exe` to the update host or GitHub Release.
4. Compute SHA256:

   ```powershell
   Get-FileHash .\dist\SafeSky.exe -Algorithm SHA256
   ```

5. Update `version.stable.json` or `version.beta.json`.
6. Run a manual update smoke test on a Windows machine:

   - Install or run the previous version.
   - Point Settings -> Updates at the metadata URL.
   - Run update check.
   - Install update.
   - Confirm `SafeSky.exe.bak` is created and the new version starts.

## Rollback

Each update backs up the old executable to `SafeSky.exe.bak` before replacement. If a release is broken:

1. Publish a fixed version immediately, or
2. Publish metadata for a previous version with `force_downgrade: true`.

Use forced downgrade only for emergency recovery. Normal update checks do not downgrade.

## Channels

- `stable`: default channel.
- `beta`: opt-in channel from Settings.

The client fetches `version.<channel>.json` from the configured base URL.
