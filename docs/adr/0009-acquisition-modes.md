# ADR 0009 — Flexible acquisition modes (installer vs prebuilt)

Date: 2026-09-25 · Status: accepted

## Context

Manifest v2 assumed archive -> ISO -> installer. Single-link prebuilt
games (one zip with a runnable tree, one direct EXE) must not be forced
through ISO/installer stages that do not exist.

## Decision

- Manifest v3 adds explicit `package_type`: `provider`,
  `archive_installer`, `archive_prebuilt`, `direct_prebuilt`,
  `iso_installer` (v1/v2 still validate; legacy maps by declared stages).
- Rules: single-link modes take exactly one source; prebuilt modes must
  NOT declare installer/ISO stages; installer modes must declare the
  installer spec; unknown modes rejected. Extension sniffing is a helper
  only — the manifest is authoritative.
- One orchestrator (`acquire.Prepare`) dispatches by effective type over
  the same persisted state machine; prebuilt walks
  ... -> ARCHIVE_EXTRACTED (place + validate launch/expected files) ->
  SOURCE_CLEANED -> READY with no installer ever executed.
- Disk (`RequiredFor`) and cleanup (`DeleteArchiveAfterPrebuiltReady`)
  are type-aware; sources die only after the placed game validates.

## Consequences

- Five documented cases in `games/examples/case{1..5}-*.json`, each with
  end-to-end hermetic tests. Provider-managed installs honestly refuse
  (future stage) instead of faking READY.
