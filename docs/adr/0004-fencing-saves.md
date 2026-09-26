# ADR 0004 — Fencing tokens + immutable save generations

Date: 2026-09-25 · Status: accepted

## Context

Disposable nodes + network partitions => zombie writers.

## Decision

Control issues monotonic fence tokens per session/lease and save
generations ordered server-side (never node clocks). Commit requires
fence match + strictly increasing generation; mismatch => blob ORPHANED,
pointer untouched. Enforced in `internal/control/session` with chaos tests.

## Consequences

- Generation 42 survives node A's zombie commit after node B advances it.
- All failure paths need deterministic transitions (20-case matrix).
