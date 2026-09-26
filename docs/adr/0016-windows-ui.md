# ADR 0016 — Windows desktop UI (PySide6 thin client)

Date: 2026-09-26 · Status: accepted

## Context

Everything was reachable only through `cmd/play` (CLI). The product goal —
open app, choose game, play — needed a real Windows launcher, while the
architecture rules held: media never crosses the control plane, no custom
streaming, no new repos/runners, backend stays Go.

## Decision

- `client/windows/` hosts a PySide6 dark launcher: Library, Add Game
  (multi-step wizard), History, Settings, connection pill, toast feedback.
  PySide6 is the only dependency; no Electron, no web stack.
- The UI is a thin client over the real API (`/v1/games`, `/v1/sessions`,
  `/v1/stats/playtime`); no second backend, no business logic in Python.
- Play reuses the existing Go flow verbatim: the UI launches
  `cmd/play play` (resolved as packaged `play-client.exe`, or
  `go run ./cmd/play` in dev) as a subprocess, parses its staged output,
  and never re-implements session orchestration. Moonlight availability is
  checked before any session is created; honest errors otherwise.
- Add Game: one-URL-per-line multiline field (1..N sources, blank lines and
  `#` comments ignored, filename inferred from the URL as the CLI does).
  Simplified choices (Prebuilt / Installer, single / multipart, ISO stage)
  map onto the real v3 package types; the wizard's validator mirrors
  `pkg/protocol` rules (prebuilt never gets an installer, direct EXE takes
  no archive stage, single-link modes take exactly one source) and review
  shows the full manifest before submit.
- Durable GUI onboarding: `POST /v1/games` now also writes the validated
  manifest into the seed directory (`PG_MANIFEST_DIR` or
  `games/manifests`) via `catalog.SaveToFile` — temp file + rename, strict
  `[A-Za-z0-9._-]` id check. The response reports `persisted` honestly;
  failures stay in-memory only and the UI says so. This is the minimal
  backend adjustment; the catalog remains the memory registry seeded from
  the same directory.
- History shows server-observed playtime only. Artwork is a deterministic
  local placeholder (stable per-game palette + initials) with optional
  user-selected local images; no scraping, no bundled assets. Settings
  persist to `%APPDATA%/personal-game/ui-settings.json` (atomic write, no
  secrets). Packaging: `scripts/build_play_client.py` builds the Go client
  binary next to the UI so end users never install Go.

## Consequences

- `go.mod` unchanged; the Go diff is catalog persistence + API response
  shape (`manifest` + `persisted`), covered by new unit tests.
- UI tests (pytest, offscreen Qt) cover parsing, manifest semantics, API
  client, play-wrapper honesty, and window behavior without any GPU.
- Live streaming proof still requires the GPU node (unchanged); the UI
  surfaces those paths honestly.
