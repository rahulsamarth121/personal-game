# ADR 0010 — Save generations with fencing; media/control separation

Date: 2026-09-25 · Status: accepted

## Context

Disposable nodes must never corrupt persistent saves, and the control
plane must never add latency to the media path.

## Decision

- Saves: control-issued monotonic generations (never node clocks);
  begin validates lease/fence and returns a scoped PUT; commit
  re-validates fence + ordering + sha, marks VALID/CHECKPOINT, advances
  the pointer; failures mark ORPHANED/CORRUPT with the pointer untouched.
  Node restore is temp -> verify -> stage -> atomic swap with rollback;
  snapshots wait for quiescence. PostgreSQL owns pointers/ordering
  (stdlib `database/sql` store; driver at deploy), R2 owns blobs (stdlib
  SigV4 presigner; crypto pinned to RFC 4231, live check at deploy).
- Re-enrollment fences sessions pinned to the superseded token, closing
  the same-node zombie window (permanent chaos regression test).
- Media/control: video/audio/input flow client <-> node only
  (Moonlight <-> Sunshine/Wolf); control APIs carry sessions, saves,
  and presigned URLs. Direct-first, relay-fallback, no redesign needed.

## Consequences

- Latest pointer provably survives the 41/42 zombie drill.
- Honest gaps: no live-R2 verification in CI; Ludusavi is a knowledge
  source for overrides, not yet a runtime backup driver.
