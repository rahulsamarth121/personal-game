# ADR 0007 — One Python runner per environment

Date: 2026-09-25 · Status: accepted

## Context

Local had `run.bat` + `run.ps1` (duplicate logic) plus a legacy
`github/local/` scaffold; Kaggle had five shell scripts. Two launcher
architectures to maintain, drifting apart.

## Decision

- ONE local runner: `personal-game/local/runner/runner.py` (stdlib only):
  discover, validate, configure, build, launch, monitor. Deleted
  `run.bat`/`run.ps1`; if a double-click shim is ever needed it must be a
  one-line delegation to `runner.py`.
- ONE Kaggle runner: `github/kaggle/runner.py` with
  `clone|diagnostics|setup|run|cleanup|all`. Deleted
  `github/kaggle/runner/*.sh`. Notebook is a 5-cell thin wrapper.
- Deleted `github/local/`; its surviving notes (prereq list, data dirs,
  never-overwrite-config) folded into `docs/operations/local.md`.
- Runners contain zero business logic; both exec the shared
  `github/cmd/agent`. No `if kaggle` in Go code.

## Consequences

- Runners verified via `py_compile` + `--help` + dry-run diagnostics.
- Go build/vet/test unaffected (no Go changes in this consolidation).
