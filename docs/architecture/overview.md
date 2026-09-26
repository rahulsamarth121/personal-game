# Architecture overview

Personal cloud gaming (GeForce NOW-inspired, single-user). Flow:

```
Library -> PLAY -> prepare node -> restore state -> launch -> stream
  -> play -> save state -> history/playtime
```

Core principles: GPU node is disposable; save data is persistent;
installs are cache; reuse mature streaming (Wolf/Sunshine/Moonlight);
orchestration is our value-add.

## Processes

- **Control plane** (Go): auth, catalog, node registry, scheduling,
  session state machine, leases/fencing, save-pointer authority,
  playtime/history, presigned URLs. Never in the video path.
- **Node agent** (Go, single binary): enroll, heartbeat/lease, capability
  discovery, acquire/cache, save restore/snapshot, process supervision,
  Wolf/Sunshine lifecycle, telemetry, cleanup.
- **Client shell**: login, library, Play, history. Moonlight handles
  decode/input; we never build a custom streaming protocol.

## Game preparation: two flows

MODE A — multipart installer (`archive_installer` / `iso_installer`):
1..N authorized parts -> download/resume/verify -> archive extract ->
ISO -> ISO extract -> installer EXE -> validated install -> READY.

MODE B — single-link prebuilt (`archive_prebuilt` / `direct_prebuilt`):
one URL -> download/verify -> extract-if-archived -> locate launch EXE
-> validate -> READY. No installer stage is ever invented here.

The manifest's `package_type` (v3) selects the flow; disk planning and
cleanup are type-aware. See `games/examples/case{1..5}-*.json`.

## Storage classes

PERSISTENT (saves, generations, history, metadata) vs WARM GAME CACHE
(installed/prebuilt games on persistent volumes where supported) vs
EPHEMERAL (downloads, extract, ISO, installers, staging). See storage.md.

## Deployment modes

LOCAL (primary gaming path) and KAGGLE share the same agent core.
Kaggle specifics stay under `kaggle/` as capability VALUES, never
`if provider == ...` in business logic.

## Key decisions

See `docs/adr/`: Go monorepo, PostgreSQL-first, R2 blobs, Tailscale phase 1,
fencing mandatory, Ludusavi for save knowledge, one Python runner per env,
explicit package types, server-observed playtime, media/control separation.
