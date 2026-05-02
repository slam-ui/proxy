# Bug review needed

These items were not changed because they are ambiguous, require product/security policy decisions, or sit in files with pre-existing uncommitted user edits that should not be captured by this audit branch.

## R-003: Kill switch startup policy conflicts with fail-close requirement
- **Coordinates:** `internal/killswitch/killswitch_windows.go:105`
- **Hypothesis:** `CleanupOnStart` removes firewall rules from a previous crash, while the audit prompt says killswitch should fail-close and unblock only on clean shutdown.
- **Why not fixed:** This is a product/security policy decision. Current code comments describe startup cleanup as intentional crash recovery, so changing it could lock users out unexpectedly.
- **Suggested fix:** Decide policy explicitly. If fail-close wins, keep rules on dirty startup and require user/admin action to disable.

## R-006: CodeRabbit CLI unavailable in this environment — RESOLVED
- **Coordinates:** baseline preparation
- **Resolution:** Verified CodeRabbit CLI 0.4.4 via WSL Ubuntu-22.04 with authenticated account `slam-ui`.
- **Follow-up:** Baseline review artifacts live in `.audit/coderabbit/`; process documentation is in `docs/CODERABBIT_PROCESS.md`.
