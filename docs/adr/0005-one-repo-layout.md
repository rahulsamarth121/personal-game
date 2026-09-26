# ADR 0005 — One repo: github/ is the source of truth

Date: 2026-09-25 · Status: accepted

## Context

Stage 0 lived directly in `personal-game/`. LOCAL and KAGGLE need one
shared implementation, with Kaggle cloning from GitHub and a thin local
launcher outside the repo.

## Decision

- `personal-game/github/` is the ONLY Git repository and source of truth
  (Go code, protocol, agent, control plane, manifests, tests, docs,
  Kaggle runner).
- `personal-game/local/runner/` (outside git) only launches the shared
  `github/cmd/agent` binary: prereq check, build, config, start, status.
- Kaggle runner lives at `github/kaggle/runner/`; the notebook is a thin
  wrapper calling those scripts. No second agent implementation anywhere.

## Consequences

- Module path unchanged (`github.com/personal-game/personal-game`); the
  move is purely a directory relocation, verified by build+tests.
- Old `local/` scaffold preserved as `github/local/` deployment notes.
- Secrets, game binaries, ISOs, saves excluded via `.gitignore`.
