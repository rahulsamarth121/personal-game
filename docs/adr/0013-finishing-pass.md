# ADR 0013 — Finishing pass: eviction, onboarding, one-terminal divisor

Date: 2026-09-25 · Status: accepted

## Context

The slice worked but left gaps: eviction was computed, never executed;
adding a game meant hand-writing JSON; local runs needed two terminals;
live-vendor coverage stopped at unit level.

## Decision

- `cache.EvictForNeed`: measured-byte LRU eviction executed from the
  handler when eviction covers the shortfall (active game + saves immune);
  registry records validated installs per run.
- `play add-game`: flag-driven v3 manifest generator (repeatable
  source/metadata flags paired by index); validates before write, refuses
  overwrite without `--force`, refuses incoherent combos (prebuilt +
  installer, multi-source single-link).
- Runner `up` mode: builds + supervises a local control plane (health
  gated) and runs the agent in one terminal; refuses remote URLs.
- Live-vendor proof where the machine allows: real aria2c download test
  (loopback httptest), R2 `PresignHead`, failure-matrix tests (resume,
  corrupt archive, installer exit codes, node-death save, upload refused,
  ISO-without-7z, wait-running timeout, classifyExit).
- README rewritten to the required operator sections; `status.md`
  distinguishes DONE / LIVE VERIFIED / MOCK VERIFIED / UNAVAILABLE.

## Consequences

- No new dependencies, no new infrastructure, no architecture change.
- Multipart reconstruction at N>1 still needs 7-Zip (honest error);
  provider installs need vendor login/entitlements at runtime.
