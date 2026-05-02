# CodeRabbit review process

## Local prerequisites

- WSL Ubuntu-22.04.
- CodeRabbit CLI available in WSL: `coderabbit --version`.
- Agent authentication: `coderabbit auth status --agent`.
- If auth is missing: `coderabbit auth login --agent`.

Current verified environment:
- CLI: `coderabbit 0.4.4` via WSL Ubuntu-22.04.
- Account: `slam-ui`.

## Baseline review

Run a full baseline after major audit passes or before a roadmap phase:

```bash
mkdir -p .audit/coderabbit
wsl -d Ubuntu-22.04 -- bash -lc \
  "cd /mnt/c/Users/13372/GolandProjects/proxy && coderabbit review --agent -c AGENTS.md" \
  > .audit/coderabbit/baseline.ndjson 2>&1
```

Summarize results in `.audit/coderabbit/baseline_summary.md`.

## Per-pass review

After each bugfix or audit pass, review only the pass diff:

```bash
wsl -d Ubuntu-22.04 -- bash -lc \
  "cd /mnt/c/Users/13372/GolandProjects/proxy && coderabbit review --agent --base main -c AGENTS.md" \
  > .audit/<topic>/coderabbit.ndjson 2>&1
```

Use the actual base branch or base commit for the pass when it is not `main`.

## Triage

Classify each finding:
- **Actionable:** confirm, add or update a regression test where practical, fix in one commit.
- **Discussable:** record in `CODERABBIT_DISCUSSION.md` with options for the user.
- **Noise:** record in `CODERABBIT_IGNORED.md` with a concrete reason.

Do not suppress or ignore findings without recording why.

## Commit format

Use one commit per actionable CodeRabbit finding:

```text
fix(<package>): <issue> (CodeRabbit CR-NNN)

Symptom: ...
Root cause: ...
Fix: ...
Test: ...
```

## Cadence

- Full baseline: monthly, before major release, or before a new roadmap phase.
- Per-pass: after every audit or bugfix prompt.
- PR review: optional via CodeRabbit GitHub integration if budget and repository settings allow it.
