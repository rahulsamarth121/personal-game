# ADR 0015 — Release readiness: audit, setup, journeys

Date: 2026-09-25 · Status: accepted

## Context

At ~96% the work was release hygiene: repo audit, node config surface,
staged UX, journey docs, and the last failure-matrix holes — not new
subsystems.

## Decision

- Audit: single repo/runners confirmed, no secrets/binaries/saves in the
  tree, one `__pycache__` removed; `.gitignore` + templates verified.
- Node config via environment only: `PG_GAME_ROOT`, `PG_TEMP_ROOT`,
  `PG_GPU_INDEX` (validated non-negative; plumbed to game processes as
  `CUDA/NVIDIA_VISIBLE_DEVICES` for Sunshine host launches).
- `play` prints staged progress per observed session state (no tokens),
  plus elapsed-time context; `pair` reuses client-side Moonlight state.
- Tailscale peer count via `status --json` (best effort, -1 unknown).
- `scripts/live_smoke.py`: PASS / BLOCKED BY ENVIRONMENT / FAIL per
  vendor, exit 1 only on present-but-broken; missing optionals never fail.
- Failure matrix closed: N=20 parts, fence-during-preparation, degraded
  reconnect, dead-endpoint HTTP, duplicate commit/close, PG-unreachable,
  migration table coverage, R2 HEAD presigning.
- README carries the 18 required operator sections incl. GPU node,
  Direct EXE, Networking, Current limitations; journeys A–E mapped to
  commands.

## Consequences

- CODE-COMPLETE here; LIVE-COMPLETE needs the GPU host + vendors.
- No new dependencies, no new services, no custom media code.
