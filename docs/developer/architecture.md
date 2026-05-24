# Architecture Deep Dive

The main architecture document is [../ARCHITECTURE.md](../ARCHITECTURE.md).
This page focuses on implementation boundaries.

## Startup Ownership

`cmd/proxy-client` is the only package that wires together platform services,
the API server, WebView2 window, tray, Wintun cleanup, and `sing-box` manager.
This keeps service packages testable.

Startup is split into parallel work:

- preflight port checks
- orphan `sing-box.exe` cleanup
- app rules loading
- Wintun cleanup
- config generation
- API startup

`sing-box` starts only after API readiness and Wintun/config readiness.

## API Route Registration

New feature routes belong in `SetupFeatureRoutes()` in `internal/api/server.go`.
This keeps legacy test helpers stable and makes route ownership visible.

Mutating handlers must use strict JSON decoding:

```go
r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
dec := json.NewDecoder(r.Body)
dec.DisallowUnknownFields()
```

## Config Builder

`internal/config` parses external server formats and builds `SingBoxConfig`.
Config changes should be validated through:

- unit tests
- golden tests for backward compatibility
- `sing-box check` where the binary is available

## Recovery Model

`internal/xray` reports process crashes to the app lifecycle. The app decides
whether to restart, clean Wintun state, or save crash reports.

## Concurrency Rules

- Long-running goroutines must observe `context.Context` or a done channel.
- `Start()` methods should be idempotent or explicitly reject duplicate starts.
- Shared state in API handlers must be protected because handlers run
  concurrently.
