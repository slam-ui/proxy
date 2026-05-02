# Bug audit fix log

## TL;DR
- Fixed: 31 (Critical: 0, High: 7, Medium: 23, Low: 1)
- Skipped (in BUG_REVIEW_NEEDED.md): 1; additional endpoint/lint review items tracked separately.
- Tools delta: build/race/vet/staticcheck unchanged green; gosec improved from 291 to 128 findings; golangci-lint clean; govulncheck clean with Go 1.26.2.

## Fixes

### [F-001] Strict failover settings body
- **Severity:** Medium
- **Category:** D
- **File(s):** internal/api/failover_handlers.go:20, internal/api/servers_handlers_test.go:444
- **Commit:** 8700dc3
- **Symptom:** `/api/servers/failover/settings` accepted unbounded JSON, unknown fields, and trailing JSON.
- **Root cause:** Handler decoded `r.Body` directly into `config.SmartFailoverSettings`.
- **Fix:** Added request size cap, `DisallowUnknownFields`, and trailing-value rejection.
- **Test:** `TestHandleSetFailoverSettingsRejectsUnknownFields`, `TestHandleSetFailoverSettingsRejectsOversizedBody`
- **Verified:** go build ok, go test -race ok, GOOS=windows/linux build ok

### [F-002] Strict manual sing-box config body
- **Severity:** Medium
- **Category:** D
- **File(s):** internal/api/singbox_config_handlers.go:56, internal/api/singbox_config_handlers_test.go:13
- **Commit:** b083ea4
- **Symptom:** Manual sing-box config updates accepted oversized or schema-loose JSON.
- **Root cause:** Direct body decode without max size or strict decoder settings.
- **Fix:** Added 4 MiB body cap, unknown-field rejection, and trailing-value rejection.
- **Test:** `TestHandleSetSingBoxConfigRejectsUnknownFields`, `TestHandleSetSingBoxConfigRejectsOversizedBody`
- **Verified:** go build ok, go test -race ok, GOOS=windows/linux build ok

### [F-003] Strict geosite download body
- **Severity:** Medium
- **Category:** C, D
- **File(s):** internal/api/geosite_handlers.go:180, internal/api/geosite_handlers_extra_test.go:81
- **Commit:** a144e28
- **Symptom:** Geosite download endpoint accepted unbounded and schema-loose JSON.
- **Root cause:** Optional request body was decoded directly.
- **Fix:** Preserved empty-body behavior but added 4 KiB cap, strict decode, and trailing-value rejection.
- **Test:** `TestHandleGeositeDownloadRejectsUnknownFields`, `TestHandleGeositeDownloadRejectsOversizedBody`
- **Verified:** go build ok, go test -race ok, GOOS=windows/linux build ok

### [F-004] Strict engine request body
- **Severity:** Medium
- **Category:** D
- **File(s):** internal/api/engine_handlers.go:20, internal/api/engine_handlers_test.go:13
- **Commit:** 968567f
- **Symptom:** Engine version/download endpoints accepted oversized, unknown-field, or multi-value JSON.
- **Root cause:** Optional body decode had no limit or strict schema.
- **Fix:** Added shared decoder with 4 KiB cap while keeping empty body valid.
- **Test:** `TestHandleEngineDownloadRejectsUnknownFields`, `TestHandleEngineVersionRejectsOversizedBody`
- **Verified:** go build ok, go test -race ok, GOOS=windows/linux build ok

### [F-005] Strict profile save body
- **Severity:** Medium
- **Category:** D
- **File(s):** internal/api/profile_handlers.go:150, internal/api/profile_handlers_test.go:18
- **Commit:** 67034fc
- **Symptom:** Profile save accepted unknown fields and arbitrarily large JSON.
- **Root cause:** `handleSaveProfile` decoded request body directly.
- **Fix:** Added 1 MiB cap, strict decode, and trailing-value rejection.
- **Test:** `TestHandleSaveProfileRejectsUnknownFields`, `TestHandleSaveProfileRejectsOversizedBody`
- **Verified:** go build ok, go test -race ok, GOOS=windows/linux build ok

### [F-006] Strict server request bodies
- **Severity:** Medium
- **Category:** D
- **File(s):** internal/api/servers_handlers.go:83, internal/api/servers_handlers_test.go:388
- **Commit:** 0563abe
- **Symptom:** Server add/import/fetch handlers accepted unbounded or schema-loose JSON.
- **Root cause:** Three handlers decoded `r.Body` directly.
- **Fix:** Added shared strict decoder with 64 KiB cap for server request bodies.
- **Test:** `TestHandleAddServerRejectsUnknownFields`, `TestHandleFetchURLRejectsOversizedBody`
- **Verified:** go build ok, go test -race ok, GOOS=windows/linux build ok

### [F-007] Clear DPAPI plaintext native buffer
- **Severity:** High
- **Category:** B, G
- **File(s):** internal/dpapi/dpapi_windows.go:31, internal/dpapi/dpapi_test.go:8
- **Commit:** d49c280
- **Symptom:** DPAPI plaintext could remain in the native `DATA_BLOB` until freed memory was reused.
- **Root cause:** `Decrypt` copied `out.pbData` then called `LocalFree` without clearing the buffer.
- **Fix:** Centralized blob cleanup and zeroed decrypted native output before `LocalFree`.
- **Test:** `TestRoundTrip`
- **Verified:** go build ok, go test -race ok, GOOS=windows/linux build ok

### [F-008] Bound GeoIP fallback HTTP client
- **Severity:** Medium
- **Category:** C, D
- **File(s):** internal/api/geoip_handlers.go:27, internal/api/geoip_handlers_test.go:55
- **Commit:** 4fec33b
- **Symptom:** GeoIP fallback used `http.DefaultClient`, which has no client-level timeout.
- **Root cause:** `countryFromIPAPI` relied only on request context cancellation.
- **Fix:** Added package HTTP client with a finite 3s timeout.
- **Test:** `TestGeoIPHTTPClientHasTimeout`
- **Verified:** go build ok, go test -race ok, GOOS=windows/linux build ok

### [F-009] Cap speedtest download reads
- **Severity:** High
- **Category:** C, G
- **File(s):** internal/speedtest/speedtest.go:42, internal/speedtest/speedtest_test.go:20
- **Commit:** b3aaa62
- **Symptom:** Speedtest treated non-2xx responses as successful and could read an oversized response until EOF.
- **Root cause:** `Run` copied `resp.Body` directly without status or byte-limit checks.
- **Fix:** Required 2xx status and limited reads to the expected 10 MB payload plus one sentinel byte.
- **Test:** `TestRunRejectsHTTPErrorStatus`, `TestRunRejectsOversizedResponse`
- **Verified:** go build ok, go test -race ok, GOOS=windows/linux build ok

### [F-010] Replace inherited proxy environment
- **Severity:** Medium
- **Category:** C, E
- **File(s):** internal/process/launcher.go:152, internal/process/launcher_test.go:118
- **Commit:** db21db5
- **Symptom:** Child processes could receive stale `HTTP_PROXY`/`NO_PROXY` values alongside SafeSky values.
- **Root cause:** Proxy mode appended new env vars without filtering; direct mode did not classify `NO_PROXY` as proxy-related.
- **Fix:** Filter inherited proxy/no_proxy variables before appending desired values.
- **Test:** `TestApplyProxyEnvReplacesExistingProxyVars`, `TestApplyDirectEnvRemovesExistingNoProxyVars`
- **Verified:** go build ok, go test -race ok, GOOS=windows/linux build ok

### [F-011] Preserve traffic counters on save failure
- **Severity:** High
- **Category:** A, E
- **File(s):** internal/trafficstats/stats.go:40, internal/trafficstats/stats_test.go:26
- **Commit:** 4db556a
- **Symptom:** In-memory traffic counters were lost if persisting `data/traffic_stats.json` failed.
- **Root cause:** `SaveToFile` swapped counters to zero before `WriteAtomic` and did not restore on write failure.
- **Fix:** Serialized `SaveToFile` and re-added swapped counters when the write fails.
- **Test:** `TestSaveToFileRestoresSessionCountersOnWriteError`, `TestSaveToFilePersistsSessionTotals`
- **Verified:** go build ok, go test -race ok, GOOS=windows/linux build ok

### [F-012] Fail rule creation on entropy errors
- **Severity:** Medium
- **Category:** G, H
- **File(s):** internal/apprules/engine.go:63, internal/apprules/engine_extra_test.go:103
- **Commit:** 8332507
- **Symptom:** Rule creation could proceed with a predictable zero-value ID if `crypto/rand` failed.
- **Root cause:** `newID` ignored `rand.Read` errors and short reads.
- **Fix:** Propagated ID generation errors through `AddRule` and `restoreRule`.
- **Test:** `TestEngine_AddRule_IDGenerationError`
- **Verified:** go build ok, go test -race ok, GOOS=windows/linux build ok

### [F-013] Audit DPAPI native cleanup
- **Severity:** Low
- **Category:** B, G
- **File(s):** internal/dpapi/dpapi_windows.go:31
- **Commit:** 5cbd9b8
- **Symptom:** The DPAPI cleanup helper introduced native-buffer clearing that security tooling could still report as unaudited unsafe pointer use.
- **Root cause:** The helper necessarily converts a DPAPI-owned `LocalAlloc` buffer to a Go slice before `LocalFree`, but the reason was not documented at the exact unsafe operations.
- **Fix:** Added narrow `#nosec G103` justifications for DPAPI-owned native buffer cleanup and explicitly discarded `LocalFree`'s returned handle.
- **Test:** `TestRoundTrip`
- **Verified:** go build ok, go test -race ok, GOOS=windows/linux build ok

## Decoder follow-up (R-001, R-002)

### [F-014] Strict app proxy request bodies
- **Severity:** Medium
- **Category:** D
- **File(s):** internal/api/app_proxy_handlers.go, internal/api/app_proxy_handlers_test.go
- **Commit:** cd5c29c
- **Symptom:** App proxy POST/PUT handlers accepted oversized, unknown-field, or multi-value JSON bodies.
- **Root cause:** Handlers decoded `r.Body` directly without per-handler request caps or strict decoder settings.
- **Fix:** Added 64 KiB body caps, `DisallowUnknownFields`, and trailing-value rejection.
- **Test:** `TestHandleCreateRuleRejectsUnknownFields`, `TestHandleCreateRuleRejectsOversizedBody`, `TestHandleUpdateRuleRejectsUnknownFields`, `TestHandleUpdateRuleRejectsOversizedBody`, `TestHandleLaunchRejectsUnknownFields`, `TestHandleLaunchRejectsOversizedBody`, `TestHandleMatchRuleRejectsUnknownFields`, `TestHandleMatchRuleRejectsOversizedBody`
- **Verified:** targeted strict decoder tests ok

### [F-015] Strict client feature request bodies
- **Severity:** Medium
- **Category:** D
- **File(s):** internal/api/client_features_handlers.go, internal/api/client_features_handlers_test.go
- **Commit:** ba85126
- **Symptom:** Client feature POST handlers accepted oversized, unknown-field, or multi-value JSON bodies.
- **Root cause:** Handlers decoded `r.Body` directly without per-handler request caps or strict decoder settings.
- **Fix:** Added 4 KiB body caps, `DisallowUnknownFields`, and trailing-value rejection.
- **Test:** `TestHandleConnectionRuleRejectsUnknownFields`, `TestHandleConnectionRuleRejectsOversizedBody`, `TestHandleDNSGuardSetRejectsUnknownFields`, `TestHandleDNSGuardSetRejectsOversizedBody`, `TestHandleTrafficBudgetSetRejectsUnknownFields`, `TestHandleTrafficBudgetSetRejectsOversizedBody`
- **Verified:** targeted strict decoder tests ok

### [F-016] Strict settings request bodies
- **Severity:** Medium
- **Category:** D
- **File(s):** internal/api/settings_handlers.go, internal/api/settings_handlers_strict_test.go
- **Commit:** b069634
- **Symptom:** Settings POST handlers accepted oversized, unknown-field, or multi-value JSON bodies.
- **Root cause:** Handlers decoded `r.Body` directly without per-handler request caps or strict decoder settings.
- **Fix:** Added 64 KiB caps for full settings/DNS payloads, 4 KiB caps for toggles, `DisallowUnknownFields`, and trailing-value rejection through a shared settings decoder.
- **Test:** `TestHandleSetSettingsRejectsUnknownFields`, `TestHandleSetSettingsRejectsOversizedBody`, `TestHandleSetProxyGuardRejectsUnknownFields`, `TestHandleSetProxyGuardRejectsOversizedBody`, `TestHandleSetAutorunRejectsUnknownFields`, `TestHandleSetAutorunRejectsOversizedBody`, `TestHandleSetStartupProxyRejectsUnknownFields`, `TestHandleSetStartupProxyRejectsOversizedBody`, `TestHandleSetKillSwitchRejectsUnknownFields`, `TestHandleSetKillSwitchRejectsOversizedBody`, `TestHandleSetDNSRejectsUnknownFields`, `TestHandleSetDNSRejectsOversizedBody`, `TestHandleSetGeositeUpdateRejectsUnknownFields`, `TestHandleSetGeositeUpdateRejectsOversizedBody`, `TestSettingsHandlersRejectTrailingData`
- **Verified:** targeted strict decoder tests ok

### [F-017] Strict TUN request bodies
- **Severity:** Medium
- **Category:** D
- **File(s):** internal/api/tun_handlers.go, internal/api/tun_handlers_strict_test.go
- **Commit:** 1d10c0a
- **Symptom:** TUN rule mutation/import endpoints accepted loose JSON bodies.
- **Root cause:** Rule add, bulk replace, default-action update, and routing import decoded request JSON without strict schema checks.
- **Fix:** Added 4 KiB caps for small mutations, 1 MiB caps for list/import payloads, `DisallowUnknownFields`, and trailing-value rejection. Routing import now applies strict decoding to both direct JSON and multipart-uploaded JSON.
- **Test:** `TestHandleAddRuleRejectsUnknownFields`, `TestHandleAddRuleRejectsOversizedBody`, `TestHandleBulkReplaceRulesRejectsUnknownFields`, `TestHandleBulkReplaceRulesRejectsOversizedBody`, `TestHandleSetDefaultRejectsUnknownFields`, `TestHandleSetDefaultRejectsOversizedBody`, `TestHandleImportRejectsUnknownFields`, `TestHandleImportRejectsOversizedBody`, `TestTunHandlersRejectTrailingData`
- **Verified:** targeted strict decoder tests ok

### [F-018] Strict imported-rules request body
- **Severity:** Medium
- **Category:** D
- **File(s):** internal/api/improvements_handlers.go, internal/api/improvements_handlers_test.go
- **Commit:** 01b8908
- **Symptom:** `/api/tun/rules/import` accepted oversized, unknown-field, or multi-value JSON bodies.
- **Root cause:** Handler decoded `r.Body` directly without a request cap or strict decoder settings.
- **Fix:** Added a 1 MiB body cap, `DisallowUnknownFields`, and trailing-value rejection.
- **Test:** `TestHandleImportRulesRejectsUnknownFields`, `TestHandleImportRulesRejectsOversizedBody`, `TestHandleImportRulesRejectsTrailingData`
- **Verified:** targeted strict decoder tests ok

### [F-019] HTTP server ReadTimeout
- **Severity:** Medium
- **Category:** D
- **File(s):** internal/api/server.go, internal/api/server_timeout_test.go
- **Commit:** 587dc9c
- **Symptom:** API server had `ReadHeaderTimeout`, `WriteTimeout`, and `IdleTimeout`, but no full request `ReadTimeout`.
- **Root cause:** `http.Server` construction did not bound time spent reading request bodies.
- **Fix:** Added a 30 second `ReadTimeout`, keeping it greater than the 5 second `ReadHeaderTimeout`. No streaming/upload route needed a disabled read deadline; API uploads are bounded JSON or multipart bodies.
- **Test:** `TestServerHasReadTimeout`
- **Verified:** targeted timeout test ok

## Concurrency pass (category A)

### [F-020] Detector lifecycle race
- **Severity:** High
- **Category:** A
- **File(s):** internal/anomalylog/detector.go, internal/anomalylog/detector_test.go
- **Commit:** cee67e0
- **Symptom:** Concurrent `Start`/`Stop` could race on `stopCh`/`stopOnce` and panic with `sync: WaitGroup is reused before previous Wait has returned`.
- **Root cause:** `Start` reset lifecycle fields while `Stop` could close or wait on the previous generation without a lifecycle mutex.
- **Fix:** Serialized detector lifecycle transitions, captured `stopCh` per goroutine generation, and made `Stop` close only the active generation.
- **Test:** `TestDetector_StartStopConcurrentNoRace`
- **Verified:** `go build ./...`, `GOOS=linux go build ./...`, `go test ./internal/anomalylog/... -count=1 -race -timeout=180s`

### [F-021] Proxy guard shutdown race
- **Severity:** Medium
- **Category:** A
- **File(s):** internal/proxy/manager.go, internal/proxy/proxy_guard_test.go
- **Commit:** f024295
- **Symptom:** `StopGuard` returned after canceling the context while the active guard check could still be executing.
- **Root cause:** Guard lifecycle had cancel state but no wait for the current guard goroutine generation.
- **Fix:** Added guard lifecycle serialization and a `WaitGroup` so `StartGuard`/`StopGuard` wait for displaced or canceled guard generations to exit.
- **Test:** `TestStopGuardWaitsForActiveCheck`
- **Verified:** `go build ./...`, `GOOS=linux go build ./...`, `go test ./internal/proxy/... -count=1 -race -timeout=180s`

### [F-022] Xray failed restart state race
- **Severity:** High
- **Category:** A
- **File(s):** internal/xray/manager.go, internal/xray/manager_test.go
- **Commit:** 2ec7ba8
- **Symptom:** A failed restart after a prior process exit could leave `done` replaced by a never-closed channel, making `Wait` block and `IsRunning` report true.
- **Root cause:** `Start`/`StartAfterManualCleanup` swapped lifecycle state before `doStart`; failure paths did not restore the previous generation.
- **Fix:** Serialized lifecycle operations and rolled back `done`/`stopped` on failed starts while waking waiters on the failed generation.
- **Test:** `TestManagerStartFailureRestoresDone`
- **Verified:** `go build ./...`, `GOOS=linux go build ./...`, `go test ./internal/xray/... -count=1 -race -timeout=180s`

### [F-023] Network protection callback race
- **Severity:** Medium
- **Category:** A
- **File(s):** internal/api/client_features_handlers.go, internal/api/client_features_handlers_test.go
- **Commit:** 90ba67b
- **Symptom:** Windows network-change callbacks could run concurrently and race on the last seen network fingerprint.
- **Root cause:** `netwatch` dispatches `onChange` in new goroutines, while `startNetworkProtection` stored `last` in an unsynchronized closure variable.
- **Fix:** Moved fingerprint state into a `networkChangeTracker` protected by a mutex.
- **Test:** `TestNetworkChangeTrackerConcurrentNoRace`
- **Verified:** `go build ./...`, `GOOS=linux go build ./...`, `go test ./internal/api/... -count=1 -race -timeout=180s`

### [F-024] Feature-route test lifecycle leak
- **Severity:** Medium
- **Category:** A
- **File(s):** internal/api/backup_restore_extra_test.go
- **Commit:** e1bbf48
- **Symptom:** Repeated `internal/api` runs accumulated background route workers because a test called `SetupFeatureRoutes(context.Background())`.
- **Root cause:** Feature routes start diagnostics/network-protection goroutines that require a cancelable lifecycle context.
- **Fix:** Switched the test to a cancelable feature context and canceled it during cleanup.
- **Test:** `TestBackupRestoreRoute_AllowsFiveMegabyteUploads`
- **Verified:** `go build ./...`, `GOOS=linux go build ./...`, `go test ./internal/api/... -count=1 -race -timeout=180s`

### [F-025] Server ping probe hang
- **Severity:** High
- **Category:** A
- **File(s):** internal/api/servers_handlers.go, internal/api/servers_handlers_test.go
- **Commit:** f34f859
- **Symptom:** `pingServerWithProbes(context.Background(), ...)` could hang in Windows DNS/TCP connect until the system resolver returned.
- **Root cause:** The function used `net.Dialer.DialContext` with the caller context but no per-probe deadline.
- **Fix:** Added a 2 second per-probe timeout and removed DNS dependency from the all-probes-fail regression.
- **Test:** `TestPingServerWithProbes_DefaultProbeTimeout`, `TestPingServerWithProbes_AllProbesFail`
- **Verified:** `go build ./...`, `GOOS=linux go build ./...`, `go test ./internal/api/... -count=1 -race -timeout=180s`

## Test-only commits

### [T-001] Check rule mutation errors
- **Commit:** 1cf0b06
- **File(s):** internal/apprules/engine_extra_test.go
- **Reason:** Existing tests in the touched file ignored rule mutation errors; checking them prevents silent false-positive test passes and removed a local `golangci-lint` finding without changing production behavior.
- **Verified:** go build ok, go test -race ok, GOOS=windows/linux build ok

## Win32 / unsafe pass (categories B, J)

### [F-026] Unsafe DLL search paths for system DLLs
- **Severity:** High
- **Category:** B
- **File(s):** cmd/proxy-client/main.go, internal/xray/process_windows.go, internal/process/monitor_windows.go, internal/proxy/windows_proxy.go, internal/window/window.go, internal/fileutil/atomic.go, cmd/proxy-client/orphan_test.go
- **Symptom:** Several Win32 callers used `syscall.NewLazyDLL`, which can participate in unsafe DLL search behavior.
- **Root cause:** System DLL loading was inconsistent across Windows-only packages.
- **Fix:** Switched Win32 system DLL loading to `windows.NewLazySystemDLL` and set default DLL directories early in `main`.
- **Verified:** `go build ./...`, `GOOS=linux GOARCH=amd64 go build ./...`, `gosec -quiet -severity high ./...`

### [F-027] Wintun DLL load path not pinned
- **Severity:** High
- **Category:** B
- **File(s):** internal/wintun/probe_windows.go
- **Symptom:** The Wintun probe loaded `wintun.dll` by name.
- **Root cause:** The probe did not constrain DLL resolution to the application directory plus System32.
- **Fix:** Load `wintun.dll` from the executable directory via `LoadLibraryEx` with `LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR|LOAD_LIBRARY_SEARCH_SYSTEM32`, then resolve exports with `GetProcAddress`.
- **Verified:** `go test ./internal/wintun/... -count=1`, full `go test ./... -count=1 -race -timeout=10m`

### [F-028] Win32 callbacks lacked panic/lifetime guards
- **Severity:** Medium
- **Category:** B
- **File(s):** internal/tray/tray_win32.go, internal/netwatch/netwatch_windows.go
- **Symptom:** Panics escaping Win32 callbacks or callback values losing lifetime clarity could destabilize the process.
- **Root cause:** Callback functions were not consistently wrapped and retained.
- **Fix:** Added callback panic recovery, a package-level tray window proc callback, and explicit `runtime.KeepAlive` where callback or UTF-16 pointer lifetime matters.
- **Verified:** `go test ./internal/tray/... ./internal/netwatch/... -count=1`

### [F-029] High-severity syscall conversion findings
- **Severity:** Medium
- **Category:** B
- **File(s):** cmd/proxy-client/main.go, internal/window/window.go, internal/tray/tray_win32.go, internal/tray/icon.go, internal/dpapi/dpapi_windows.go, internal/xray/memory_windows.go
- **Symptom:** `gosec` high severity G115 findings obscured real integer-conversion risk in syscall arguments.
- **Root cause:** Win32 signed integer parameters and bounded buffer lengths were cast inline without guards or rationale.
- **Fix:** Added bounded conversion helpers, explicit length checks, and narrow `#nosec` comments only where the Win32 ABI requires signed values in `uintptr` slots.
- **Verified:** `gosec -quiet -severity high ./...`

## Security pass (category G)

### [F-030] Local API could bind wildcard addresses
- **Severity:** High
- **Category:** G
- **File(s):** internal/api/server.go, internal/api/server_timeout_test.go, cmd/proxy-client/app.go, cmd/proxy-client/main.go
- **Symptom:** API defaults and internal callers mixed wildcard/localhost addressing.
- **Root cause:** `NewServer` accepted wildcard listen addresses directly, and internal URLs used `localhost`.
- **Fix:** Normalize wildcard and localhost listen addresses to `127.0.0.1`, and use loopback TCP helpers for internal client calls.
- **Test:** `TestServerNormalizesWildcardListenAddressToLoopback`
- **Verified:** `go test ./internal/api/... ./cmd/proxy-client/... -count=1`

### [F-031] Backup restore accepted unexpected in-tree files
- **Severity:** High
- **Category:** G
- **File(s):** internal/api/backup_handlers.go, internal/api/backup_restore_extra_test.go
- **Symptom:** A valid backup zip could restore arbitrary root files if the path stayed under the workdir.
- **Root cause:** Restore protection blocked Zip Slip but did not restrict entries to the backup schema.
- **Fix:** Added a restore allowlist for `servers.json`, `data/settings.json`, `data/routing.json`, and `profiles/*.json`.
- **Test:** `TestHandleBackupRestore_SkipsUnexpectedRootFiles`
- **Verified:** `go test ./internal/api/... -count=1`

### [F-032] Reserved Windows profile names
- **Severity:** Medium
- **Category:** G
- **File(s):** internal/api/profile_handlers.go, internal/api/profile_handlers_test.go
- **Symptom:** Profile names such as `CON` or `COM1` could map to reserved Windows device names.
- **Root cause:** Profile path validation rejected traversal but not Windows device basenames.
- **Fix:** Reject reserved device names before appending `.json`.
- **Test:** `TestProfilePathRejectsWindowsDeviceNames`
- **Verified:** `go test ./internal/api/... -count=1`

### [F-033] Secret key plaintext lifetime
- **Severity:** Medium
- **Category:** G
- **File(s):** internal/config/vless.go
- **Symptom:** DPAPI plaintext byte buffers could remain live after read/write operations.
- **Root cause:** DPAPI plaintext slices were converted or written without zeroing temporary buffers.
- **Fix:** Zero DPAPI plaintext and serialized secret buffers after use while keeping the returned string behavior unchanged.
- **Verified:** `go test ./internal/config/... -count=1`, `govulncheck ./...`

### [F-034] GeoIP request input hardening
- **Severity:** Medium
- **Category:** G
- **File(s):** internal/api/geoip_handlers.go
- **Symptom:** Dynamic URL construction produced high-severity security scanner findings.
- **Root cause:** The handler used raw input in the path even though callers were expected to pass IPs.
- **Fix:** Parse and normalize input with `net.ParseIP` before constructing the fixed-host request URL.
- **Verified:** `gosec -quiet -severity high ./...`

## UI pass (category I)

### [F-035] Static script loader used document.write
- **Severity:** Medium
- **Category:** I
- **File(s):** internal/api/static/app.js, internal/api/static_ui_test.go
- **Symptom:** The split frontend loader depended on `document.write`.
- **Root cause:** Script loading was synchronous and parser-dependent.
- **Fix:** Replaced it with a constant script list and sequential dynamic script injection; added a static test to prevent regression.
- **Test:** `TestStaticIndexUsesSplitAssets`
- **Verified:** `go test ./internal/api/... -count=1`

### [F-036] Frontend API calls had no default timeout
- **Severity:** Medium
- **Category:** I
- **File(s):** internal/api/static/js/00-core.js, internal/api/static_ui_test.go
- **Symptom:** Hung fetches could leave UI actions waiting indefinitely.
- **Root cause:** Browser fetch calls relied on default network behavior.
- **Fix:** Added a global fetch wrapper with `AbortController` timeout unless the caller supplies a signal, and changed the API base URL to `127.0.0.1`.
- **Test:** `TestStaticCoreFetchesLoopbackWithTimeout`
- **Verified:** `go test ./internal/api/... -count=1`

## Lint / CI / build pass (categories H, J)

### [F-037] Lint baseline hid unchecked errors
- **Severity:** Medium
- **Category:** H
- **File(s):** internal/**, cmd/proxy-client/**
- **Symptom:** `golangci-lint run ./...` failed on unchecked test setup errors, response decoders, cleanup calls, and Win32 best-effort calls.
- **Root cause:** Tests and syscall wrappers often ignored returned errors implicitly.
- **Fix:** Checked meaningful setup/decode errors, marked best-effort cleanup explicitly, added shared test helpers, and routed ignored Win32 calls through small helper wrappers.
- **Verified:** `golangci-lint run ./... --timeout 5m`

### [F-038] CI did not enforce current security gates
- **Severity:** High
- **Category:** J
- **File(s):** .github/workflows/ci.yml, go.mod
- **Symptom:** CI used an older Go selector, scoped race tests to a subset, allowed `govulncheck` failures, and uploaded non-failing gosec SARIF only.
- **Root cause:** Security/lint steps were configured as advisory or partial gates.
- **Fix:** Pin toolchain selection to Go 1.26.2, run full `go test ./... -race`, install current staticcheck/govulncheck, make govulncheck fail, and add a high-severity gosec gate.
- **Verified:** `go test ./... -count=1 -race -timeout=10m`, `govulncheck ./...`, `gosec -quiet -severity high ./...`

### [F-039] PowerShell build scripts did not consistently fail on native command errors
- **Severity:** Medium
- **Category:** J
- **File(s):** build.ps1, dev.ps1, monitor.ps1, test.ps1, run_tests.ps1, test-proxy.ps1
- **Symptom:** Native command failures could be missed in helper scripts, and local API checks used `localhost`.
- **Root cause:** Scripts lacked strict mode or `$LASTEXITCODE` handling in several paths.
- **Fix:** Added strict mode/progress suppression, introduced `Invoke-Native` in `dev.ps1`, checked `go tool cover`, and switched probe URLs to `127.0.0.1`.
- **Verified:** PowerShell script parse check, `pwsh -NoProfile -File .\dev.ps1 help`

## VLESS transport support (feature)

### [F-040] VLESS URL parser keeps real-world transport parameters
- **Severity:** Medium
- **Category:** Feature
- **File(s):** internal/config/vless.go, internal/config/singbox_types.go, internal/config/vless_transports_test.go
- **Commit:** 70884e2
- **Symptom:** VLESS links with `type`, `serviceName`, `mode`, `path`, `host`, `alpn`, `security`, or `insecure` were silently downgraded to TCP+Reality.
- **Root cause:** Parser only extracted the original TCP+Reality subset and dropped all unknown query fields.
- **Fix:** Added explicit parsing/validation for `encryption`, `security`, ALPN, insecure mode, `spx` warning, transport type, header type, path/host/serviceName, gRPC mode, and WS early data.
- **Test:** Parser tests for base fields, ws, grpc, http/h2, httpupgrade, tcp+http-obf, invalid transport/header/encryption, and warnings.
- **Verified:** `go build ./...`, `GOOS=linux go build ./...`, `go test ./internal/config/... -count=1 -race -timeout=120s`

### [F-041] VLESS TCP transport and HTTP obfuscation
- **Severity:** Medium
- **Category:** Feature
- **File(s):** internal/config/singbox_builder.go, internal/config/vless_transports_test.go
- **Commit:** e84ef96
- **Symptom:** `type=tcp&headerType=http` links were treated as plain TCP.
- **Root cause:** `SBOutbound` had no `transport` block and builder had no transport-specific branch.
- **Fix:** Added `SBTransport`, URL ALPN override, `tls.insecure`, `security=none` TLS disablement, and HTTP transport emulation for xray `headerType=http`.
- **Test:** `TestBuildVLESSOutbound_TCPHTTPObfuscation`, `TestBuildVLESSOutbound_ALPNFromURL`, `TestBuildVLESSOutbound_Insecure`
- **Verified:** `go build ./...`, `GOOS=linux go build ./...`, `go test ./internal/config/... -count=1 -race -timeout=120s`

### [F-042] VLESS WebSocket transport
- **Severity:** Medium
- **Category:** Feature
- **File(s):** internal/config/singbox_builder.go, internal/config/vless_transports_test.go
- **Commit:** 5c1e926
- **Symptom:** `type=ws` links lost `path`, `host`, and Cloudflare early data.
- **Root cause:** Builder always emitted a TCP outbound without transport metadata.
- **Fix:** Added `transport.type=ws`, Host header mapping, default `/` path, and `max_early_data` / `early_data_header_name` from `path?ed=N`.
- **Test:** `TestParseVLESSURL_WS`, `TestBuildVLESSOutbound_WS`
- **Verified:** `go build ./...`, `GOOS=linux go build ./...`, `go test ./internal/config/... -count=1 -race -timeout=120s`

### [F-043] VLESS gRPC transport
- **Severity:** Medium
- **Category:** Feature
- **File(s):** internal/config/singbox_builder.go, internal/config/vless_transports_test.go
- **Commit:** 80af96e
- **Symptom:** `type=grpc` links silently became TCP and could never connect to gRPC-framed servers.
- **Root cause:** `serviceName` and `mode` were ignored and no gRPC transport block was generated.
- **Fix:** Required `serviceName`, recorded user `mode`, warned when non-`gun` mode is ignored, and emitted `transport.type=grpc` with `service_name`.
- **Test:** `TestParseVLESSURL_GRPC`, `TestBuildVLESSOutbound_GRPC`, `TestParseVLESSURL_RejectsGRPCWithoutServiceName`
- **Verified:** `go build ./...`, `GOOS=linux go build ./...`, `go test ./internal/config/... -count=1 -race -timeout=120s`

### [F-044] VLESS HTTP/H2 transport
- **Severity:** Medium
- **Category:** Feature
- **File(s):** internal/config/singbox_builder.go, internal/config/vless_transports_test.go
- **Commit:** 20e1422
- **Symptom:** `type=http` / `type=h2` links lost host/path metadata.
- **Root cause:** Parser did not normalize `h2`, and builder had no HTTP transport branch.
- **Fix:** Normalized `type=h2` to sing-box `http`, split comma-separated hosts, required TLS for HTTP transport, and emitted method/path/host.
- **Test:** `TestParseVLESSURL_HTTP`, `TestBuildVLESSOutbound_HTTP`
- **Verified:** `go build ./...`, `GOOS=linux go build ./...`, `go test ./internal/config/... -count=1 -race -timeout=120s`

### [F-045] VLESS HTTPUpgrade transport
- **Severity:** Medium
- **Category:** Feature
- **File(s):** internal/config/singbox_builder.go, internal/config/vless_transports_test.go
- **Commit:** e9d284f
- **Symptom:** `type=httpupgrade` links were emitted as plain TCP.
- **Root cause:** No builder support existed for HTTPUpgrade transport.
- **Fix:** Added `transport.type=httpupgrade`, default `/` path, and Host header mapping accepted by sing-box 1.13.0.
- **Test:** `TestParseVLESSURL_HTTPUpgrade`, `TestBuildVLESSOutbound_HTTPUpgrade`
- **Verified:** `go build ./...`, `GOOS=linux go build ./...`, `go test ./internal/config/... -count=1 -race -timeout=120s`

### [F-046] VLESS transport schema and backward-compat checks
- **Severity:** Medium
- **Category:** Feature
- **File(s):** internal/config/vless_singbox_check_test.go, internal/config/vless_transports_test.go
- **Commit:** b3b9a60, 66e3a50
- **Symptom:** Transport JSON changes needed protection against sing-box schema drift and accidental TCP+Reality output changes.
- **Root cause:** Existing tests did not run `sing-box check` for generated transport configs or pin the old outbound JSON.
- **Fix:** Added a golden test for old TCP+Reality outbound JSON and a `sing-box check` integration test for tcp+reality, tcp+tls, tcp+http-obf, ws, grpc, http/h2, and httpupgrade.
- **Test:** `TestBuildVLESSOutbound_TCPRealityGolden`, `TestSingBoxCheck_VLESSTransports`
- **Verified:** `go build ./...`, `GOOS=linux go build ./...`, `go test ./internal/config/... -count=1 -race -timeout=120s`

Summary:
- Supported transports: tcp, tcp+http-obf, ws, grpc, http (h2), httpupgrade
- Rejected with explicit error: quic, kcp, mkcp
- Warnings on use: alpn fallback, insecure, grpc mode=multi, spx ignored
- Tests added: 20 named tests plus transport subcases
- Backward compat: verified by golden test for tcp+reality

## Cleanup pass before roadmap (categories B, G, H, I)

### [F-047] CI toolchain selection pinned for govulncheck
- **Severity:** Medium
- **Category:** J
- **File(s):** .github/workflows/ci.yml
- **Commit:** bfcbce3
- **Symptom:** `govulncheck` could report Go 1.26.1 standard-library CVEs even though `go.mod` declared `toolchain go1.26.2`.
- **Root cause:** CI pinned `setup-go` to Go 1.26.2 but did not force the Go command's toolchain selection.
- **Fix:** Added workflow-level `GOTOOLCHAIN=go1.26.2`.
- **Test:** `GOTOOLCHAIN=go1.26.2 govulncheck ./...`
- **Verified:** `go build ./...`, `GOOS=linux go build ./...`, govulncheck clean

### [F-048] Window Win32 unsafe audit consolidation
- **Severity:** Medium
- **Category:** B
- **File(s):** internal/window/window.go
- **Commit:** f2eaace
- **Symptom:** `gosec` reported 36 unaudited `G103` findings around DWM, RECT, WINDOWPLACEMENT, and WebView HWND conversions.
- **Root cause:** Inline `unsafe.Pointer` conversions repeated audited Win32 lifetime assumptions at each call site.
- **Fix:** Centralized conversions in small helpers with narrow `#nosec G103` rationale and `runtime.KeepAlive` where needed.
- **Test:** package has no test files
- **Verified:** `go build ./...`, `GOOS=linux go build ./...`, `go test ./internal/window/... -count=1 -race -timeout=180s`, gosec total 164 -> 128

### [F-049] Tray owned handle cleanup
- **Severity:** High
- **Category:** B
- **File(s):** internal/tray/tray_win32.go
- **Commit:** 4d84c3f
- **Symptom:** Repeated degraded/critical tray icon updates and tray shutdown could leave owned `HICON`/GDI handles alive.
- **Root cause:** The tray did not distinguish `LR_SHARED` resource icons from `CreateIconFromResource*` handles, and cached menu brush/font objects had no shutdown cleanup.
- **Fix:** Track icon ownership, destroy replaced owned icons, and release cached menu brush/fonts on tray exit.
- **Test:** existing tray package tests
- **Verified:** `go build ./...`, `GOOS=linux go build ./...`, `go test ./internal/tray/... -count=1 -race -timeout=180s`

### [F-050] UI listener lifecycle audit
- **Severity:** Medium
- **Category:** I
- **File(s):** internal/api/static/js/30-rules-processes.js, internal/api/static/js/50-settings-theme.js, internal/api/static/js/70-runtime-polling.js, internal/api/static/js/80-chart-utils-init.js
- **Commit:** cleanup-docs
- **Symptom:** The frontend had 14 `addEventListener` calls and no `removeEventListener` calls.
- **Root cause:** The previous audit did not classify stable global bindings versus dynamic DOM bindings.
- **Fix:** Verified listeners are attached once to stable globals (`window`/`document`), one static canvas, static pages, and a single setup input during initialization. Dynamic repeated connection rows use delegated document listeners rather than per-row handlers.
- **Test:** static audit; no code change required
- **Verified:** `rg -n "addEventListener|removeEventListener" internal/api/static/js`

### [F-051] UI/server endpoint diff audit
- **Severity:** Medium
- **Category:** D
- **File(s):** ENDPOINT_REVIEW_NEEDED.md, .audit/cleanup/ui_routes.txt, .audit/cleanup/server_routes.txt, .audit/cleanup/endpoint_diff.txt
- **Commit:** cleanup-docs
- **Symptom:** The UI/server route diff had not been captured after frontend split.
- **Root cause:** Route extraction needed to account for `API` prefixes and dynamic path segments.
- **Fix:** Captured route artifacts, confirmed apparent UI misses are dynamic route false positives, and documented 38 server-only endpoints for product review instead of deleting them.
- **Test:** `go test ./internal/api/... -count=1 -race -timeout=180s`
- **Verified:** UI direct routes 52, server routes 87, no confirmed UI-to-404 mismatch

### [F-052] Win32 lifecycle follow-up audit
- **Severity:** Medium
- **Category:** B
- **File(s):** internal/api/procicon_handler_windows.go, internal/wintun/probe_windows.go, internal/wintun/wintun.go, internal/tray/tray_win32.go
- **Commit:** cleanup-docs
- **Symptom:** The pre-roadmap prompt called out procicon, Wintun lifecycle, and tray resources for follow-up.
- **Root cause:** Earlier fixes did not record a single confirmation pass for these Windows resource surfaces.
- **Fix:** Confirmed `procicon` has paired `DestroyIcon`, `DeleteDC`, and `DeleteObject`; confirmed Wintun probe closes adapters after delete/open checks and polling paths are context-cancellable; fixed tray owned icon/GDI cleanup in F-049.
- **Test:** existing API, Wintun, and tray tests
- **Verified:** `go test ./internal/api/... ./internal/tray/... ./internal/wintun/... -count=1 -race -timeout=180s`
