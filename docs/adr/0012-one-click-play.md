# ADR 0012 — One-click play: auto-pickup, real stores, first provider

Date: 2026-09-25 · Status: accepted

## Context

Playing required copying session IDs between terminals; stores were
memory-only; acquisition had exactly one provider family.

## Decision

- Agent work loop: after enroll/heartbeat, the node polls
  `GET /v1/nodes/{id}/sessions` and runs `NODE_ASSIGNED`/fresh
  `PREPARING` sessions itself (token match enforced). `PG_SESSION_ID`
  remains as developer override. `cmd/play` becomes `library|play|history`
  with automatic Moonlight launch when the binary exists (`--print-only`
  otherwise).
- PostgreSQL via pure-Go `lib/pq`: `SessionStore` + existing `SaveStore`
  behind the same interfaces, migration embedded (`migrations.go`),
  selected by `PG_DATABASE_URL` (memory fallback otherwise). Compose runs
  postgres + minio (+ bucket init) + control-plane (Dockerfile).
- Object config standardized on `R2_*` (`R2_ENDPOINT` override targets
  MinIO); gated live tests (`PG_DATABASE_URL`, `PG_LIVE_OBJECT_TEST`)
  skip safely without services.
- SteamCMD provider: manifest `reference.app_id` (+login/validate/
  timeout), fixed argv, install-dir verification, idempotent reruns.
  Auth/DRM stay Steam's; failures name the cause.
- Cache registry (`state/cache.json`): installed-game index for LRU
  eviction and warm-cache accounting; files stay authoritative, saves
  never recorded. Handler protects/touches/releases per run.
- Tailscale detail (installed/running/IP with reasons) logged at agent
  start and shown by runner diagnostics.

## Consequences

- `go.mod` gains its first dependency (`lib/pq`, `go.sum` updated).
- Live PG/MinIO proof requires `deploy/compose` on a Docker host;
  unit/integration suites stay hermetic.
