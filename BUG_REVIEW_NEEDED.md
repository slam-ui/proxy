# Bug review needed

These items were not changed because they are ambiguous, require product/security policy decisions, or sit in files with pre-existing uncommitted user edits that should not be captured by this audit branch.

## R-003: Kill switch startup policy conflicts with fail-close requirement
- **Coordinates:** `internal/killswitch/killswitch_windows.go:105`
- **Hypothesis:** `CleanupOnStart` removes firewall rules from a previous crash, while the audit prompt says killswitch should fail-close and unblock only on clean shutdown.
- **Why not fixed:** This is a product/security policy decision. Current code comments describe startup cleanup as intentional crash recovery, so changing it could lock users out unexpectedly.
- **Suggested fix:** Decide policy explicitly. If fail-close wins, keep rules on dirty startup and require user/admin action to disable.

## R-006: CodeRabbit CLI unavailable in this environment
- **Coordinates:** baseline preparation
- **Hypothesis:** CodeRabbit review could catch additional issues beyond local static analysis.
- **Why not fixed:** `coderabbit --version` failed with command not found; no CodeRabbit CLI was available to run.
- **Suggested fix:** Install/configure the CodeRabbit CLI or run the plugin-backed review in an environment where the command is available.
