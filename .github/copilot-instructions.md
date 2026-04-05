# Workspace Instructions for proxy-client

This repository is a Windows-only Go proxy client built around `sing-box`, with a TUN interface, routing engine, Web UI, and a REST API.

## Use these instructions when working in this repository

- The repository is primarily a Windows application. Prioritize PowerShell-based workflows and preserve Windows-specific behavior.
- Focus on the `cmd/proxy-client` entrypoint, the `internal/` packages, and the `build.ps1` / `run_tests.ps1` scripts.
- Prefer existing patterns for configuration, routing, logging, and crash recovery.
- Keep changes isolated to the appropriate package and update tests in the same package.

## What this repo contains

- `cmd/proxy-client/` — main application and executable entrypoint
- `internal/api/` — REST API handlers and Web UI server
- `internal/apprules/` — routing engine, rule matching, storage, and evaluation
- `internal/config/` — configuration parsing, `sing-box` builder, VLESS and TUN support
- `internal/xray/` — sing-box process lifecycle, monitoring, restart logic, and crash handling
- `internal/proxy/` — Windows system proxy management and guard recovery
- `internal/*` — supporting services: `eventlog`, `anomalylog`, `logger`, `netutil`, `process`, `wintun`, `turnproxy`, etc.

## Common commands

- Build: `.uild.ps1` or `.uild.ps1 -Release`
- Run tests: `.











































- If the task requires new documentation, keep it concise and align with the existing Russian / English style.- If the task affects packaging or release, use `.uild.ps1 -Release`.- If the task affects config or routing rules, reference the existing `routing.json` and `geosite` support.- If the task touches runtime behavior, recommend testing with `.
un_tests.ps1` and validating with the race detector.## When the user asks for help- `internal/api/` — server handlers and endpoint contracts- `internal/proxy/` — Windows proxy enable/disable and guard logic- `internal/xray/` — sing-box process lifecycle and crash restart handling- `internal/apprules/` — rule engine and matcher behavior- `internal/config/` — config parsing and sing-box build logic- `build.ps1`, `dev.ps1`, `run_tests.ps1` — development and validation entrypoints- `Readme.md` — project overview, setup, routing semantics, API endpoints## Useful files to inspect first- When adding features, update `internal/api/` handlers and REST contracts only if the feature needs UI/API exposure.- Preserve existing error handling and anomaly logging patterns.- Avoid broad refactors that touch unrelated internal packages unless the issue requires it.- Keep platform-specific logic inside Windows-only packages or guarded code.## When editing code- The repository uses a mix of English code and Russian documentation in `Readme.md`.- The Web UI listens on `localhost:8080` and exposes HTTP proxy on `127.0.0.1:10807`, Clash API on `127.0.0.1:9090`, and TUN interface `tun0`.- LAN ranges are always bypassed automatically; avoid changes that break built-in direct bypass rules.- Routing rules are evaluated top-to-bottom and first-match wins. Preserve rule ordering semantics during modifications.- `secret.key`, `routing.json`, `geosite-*.bin`, and `sing-box.exe` are user/runtime files. Do not overwrite them unless the change is explicitly about setup or documentation.## Important repository conventions- Prefer the existing test styles and helpers found throughout `internal/*/*_test.go`.- Use `go test -race -timeout=120s ./...` as the baseline for package-level validation.- When changing package logic, add or update tests in the corresponding `*_test.go` files.- The project uses Go tests across packages and often runs with the race detector.## Testing guidance- Development helpers: `.
ev.ps1` if available or `.uild.ps1` / `.
un_tests.ps1` directly- Fuzz: `.
un_tests.ps1 fuzz 60`- Coverage: `.
un_tests.ps1 coverage`un_tests.ps1`