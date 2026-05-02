# Lint review needed

The cleanup pass reduced actionable scanner output without broad suppressions. Remaining `gosec` findings are grouped here for later targeted work instead of mass-editing high-risk Win32 and file/process call sites.

## R-001: Remaining gosec findings after cleanup
- **Coordinates:** `.audit/cleanup/gosec_after.txt`
- **Hypothesis:** Most remaining findings are audit-required Win32 `unsafe.Pointer` calls (`G103`) or broad scanner warnings that need per-call context before changing behavior.
- **Why not fixed:** The pass target is met (`gosec` total 164 -> 128, below the prompt's `<150` threshold). Additional reductions would require touching large Win32/API/process surfaces and risk unrelated behavior changes.
- **Suggested fix:** Continue in package-scoped passes: tray/process/procicon `G103`, API/config path validation `G304`, process launches `G204`, file permissions `G301/G302/G306`, and unhandled cleanup errors `G104`.
- **Confidence:** Confirmed

Current grouped totals:
- `G103`: 60
- `G304`: 21
- `G204`: 13
- `G104`: 12
- `G301`: 10
- `G302`: 6
- `G306`: 5
- `G705`: 1

`golangci-lint run ./... --timeout 5m` is clean after the tray follow-up.
