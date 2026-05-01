# Bug review needed

These items were not changed because they are ambiguous, require product/security policy decisions, or sit in files with pre-existing uncommitted user edits that should not be captured by this audit branch.

## R-001: Dirty API handlers still have loose JSON decoders
- **Coordinates:** `internal/api/app_proxy_handlers.go`, `internal/api/client_features_handlers.go`, `internal/api/settings_handlers.go`, `internal/api/tun_handlers.go`, `internal/api/improvements_handlers.go`
- **Hypothesis:** Several POST handlers still decode `r.Body` directly and likely need the same `MaxBytesReader` + `DisallowUnknownFields` + trailing-value rejection pattern applied in this audit.
- **Why not fixed:** These files had pre-existing uncommitted edits before this audit started. Committing only my hunks without absorbing unrelated user changes would require a separate coordination step.
- **Suggested fix:** After the current user edits are settled, add per-endpoint body caps and strict decoders with focused 400-response tests.

## R-002: `internal/api/server.go` lacks `ReadTimeout`
- **Coordinates:** `internal/api/server.go:316`
- **Hypothesis:** The HTTP server has `ReadHeaderTimeout`, `WriteTimeout`, and `IdleTimeout`, but no full `ReadTimeout`; slow request bodies could hold connections longer than intended.
- **Why not fixed:** `server.go` had pre-existing uncommitted edits. The change is straightforward but should not be mixed into this branch without staging unrelated user modifications.
- **Suggested fix:** Add a conservative `ReadTimeout` and update server timeout tests if present.

## R-003: Kill switch startup policy conflicts with fail-close requirement
- **Coordinates:** `internal/killswitch/killswitch_windows.go:105`
- **Hypothesis:** `CleanupOnStart` removes firewall rules from a previous crash, while the audit prompt says killswitch should fail-close and unblock only on clean shutdown.
- **Why not fixed:** This is a product/security policy decision. Current code comments describe startup cleanup as intentional crash recovery, so changing it could lock users out unexpectedly.
- **Suggested fix:** Decide policy explicitly. If fail-close wins, keep rules on dirty startup and require user/admin action to disable.

## R-004: Remaining baseline lint/security findings
- **Coordinates:** `.audit/baseline_lint.txt`, `.audit/baseline_gosec.txt`, `.audit/final_lint_after_testfix.txt`, `.audit/final_gosec_after_testfix.txt`
- **Hypothesis:** Baseline `golangci-lint` and `gosec` outputs contain many existing findings, including unchecked errors and integer conversion warnings. This audit reduced `gosec` findings from 291 to 289 but did not attempt bulk triage.
- **Why not fixed:** Many findings are in tests or Windows syscall code and need triage to separate false positives from bugs. Bulk suppression would violate the no-cosmetic/no-disable rule.
- **Suggested fix:** Triage findings package by package, adding precise `nolint` comments only where the syscall contract makes the warning false positive.

## R-005: Go toolchain vulnerability baseline
- **Coordinates:** `.audit/baseline_vuln.txt`
- **Hypothesis:** `govulncheck` reports reachable vulnerabilities in Go 1.26.1 standard library packages, fixed in Go 1.26.2.
- **Why not fixed:** This repository targets Go 1.24 in docs and the local toolchain selection is outside this code-only patch.
- **Suggested fix:** Upgrade the build toolchain to a patched Go release and rerun `govulncheck ./...`.

## R-006: CodeRabbit CLI unavailable in this environment
- **Coordinates:** baseline preparation
- **Hypothesis:** CodeRabbit review could catch additional issues beyond local static analysis.
- **Why not fixed:** `coderabbit --version` failed with command not found; no CodeRabbit CLI was available to run.
- **Suggested fix:** Install/configure the CodeRabbit CLI or run the plugin-backed review in an environment where the command is available.
