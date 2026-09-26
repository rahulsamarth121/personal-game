# ADR 0011 — Vertical slice: orchestrate Wolf, stream via Moonlight

Date: 2026-09-25 · Status: accepted

## Context

All orchestration existed, but nothing executed the end-to-end flow:
catalog -> session -> prepare -> restore -> backend -> Moonlight -> save.

## Decision

- `GamingBackend` seam (`Start/Stop/Status/ConnectionInfo/Health` as
  Probe/Start/Status/Stop/Connection): WolfBackend first (verified
  Unix-socket endpoints from Wolf's own `endpoints.cpp`: apps list,
  sessions list/stop, pair pending/client, clients), SunshineBackend as
  supervised host-process fallback, FakeBackend explicitly MOCK.
- Wolf sessions are Moonlight-driven: agent verifies app + pairing,
  publishes connection info, observes real sessions; READY requires
  verification, STREAMING requires a live session/process.
- Agent `session.Handler` runs prepare (warm-cache aware, real disk
  gate) -> save restore -> backend -> observe/close/final-save with
  fence kills; `cmd/play` is the thin client shell (verified
  `moonlight stream <host> "<app>"`).
- Catalog seeds from `games/manifests/*.json` at startup; a safe
  user-supplied `test-game-local.json` (file:// URL) is the local slice
  game — no binaries in git. `file://` sources added to the downloader.
- Ludusavi via verified CLI (`backup|restore --force --path <dir>
  <title>`); `wrap` deliberately unused (would hide the game process).
  Generations/fencing stay ours.

## Consequences

- E2E test runs the whole slice against the real HTTP API (only the
  backend faked). No Wolf/Sunshine on this machine: Probe reports
  UNAVAILABLE; live verification is a documented deploy step.
