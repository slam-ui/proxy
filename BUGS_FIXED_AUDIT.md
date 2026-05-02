# Bug audit fix log

## TL;DR
- Fixed: 19 (Critical: 0, High: 3, Medium: 15, Low: 1)
- Skipped (in BUG_REVIEW_NEEDED.md): 4
- Tools delta: build/race/vet/staticcheck unchanged green; gosec improved from 291 to 289 findings; golangci-lint and govulncheck had baseline failures not introduced here.

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

## Test-only commits

### [T-001] Check rule mutation errors
- **Commit:** 1cf0b06
- **File(s):** internal/apprules/engine_extra_test.go
- **Reason:** Existing tests in the touched file ignored rule mutation errors; checking them prevents silent false-positive test passes and removed a local `golangci-lint` finding without changing production behavior.
- **Verified:** go build ok, go test -race ok, GOOS=windows/linux build ok
