# Contributing

## Branches

Use a feature branch for each large task. Do not work directly on `main`.

Recommended prefixes:

- `feat/<topic>`
- `fix/<topic>`
- `chore/<topic>`
- `docs/<topic>`

## Commit Format

Use one behavior change per commit.

```text
fix(package): short imperative summary

Symptom: what was observed
Root cause: where and why
Fix: what changed
Test: exact test command or test name
```

## Code Style

- Follow existing package patterns.
- Keep Windows-only code behind build tags.
- Wrap errors with context.
- Do not log secrets or full server URLs.
- Use strict JSON decoding for mutating HTTP handlers.
- Add regression tests for bug fixes.
- Run race tests for concurrency changes.

## Required Checks

After each commit:

```powershell
go build ./...
$env:GOOS='linux'; $env:GOARCH='amd64'; go build ./...
Remove-Item Env:GOOS -ErrorAction SilentlyContinue
Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
go test ./<changed-package>/... -race -count=1 -timeout=180s
```

Every fifth commit:

```powershell
go test ./... -race -count=1 -timeout=300s
```

## Security Rules

- Never commit `secret.key`, generated runtime configs, logs, or packaged
  binaries.
- Validate file paths before using user-controlled path segments.
- Keep API bind address on `127.0.0.1`.
- Do not disable CI checks to get a green build.
