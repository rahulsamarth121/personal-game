# ADR 0008 — Session API, capability scheduling, server-observed playtime

Date: 2026-09-25 · Status: accepted

## Context

Stage 2 needs session lifecycle without committing to Postgres operations
yet (migration 0001 already defines the schema).

## Decision

- `internal/control/session`: `Store` interface + `MemoryStore` default;
  `Manager` (create/observe/close/playtime) with injected clock.
- Scheduling (`PickNode`): healthy, IDLE/PREPARING, caps satisfy manifest;
  most-recently-seen wins; nil (503) when nothing qualifies.
- Playtime accrues only from node observations: STREAMING+gameActive ->
  active, PREPARING/READY -> prepare; single-tick cap (120s) blocks
  phantom hours after outages. Client durations never trusted.
- Fence revalidated on observe AND close: zombies accrue nothing and
  cannot rewrite history.
- API: `POST/GET /v1/sessions`, `GET/DELETE /v1/sessions/{id}`,
  `GET /v1/stats/playtime`; structured errors map to codes.

## Consequences

- PostgreSQL wiring plugs behind `Store` later; no caller changes.
- Remaining: catalog seeding endpoint, node-side observe calls, R2 saves.
