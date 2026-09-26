# ADR 0006 — Acquisition: orchestrate mature tools, explicit pipeline

Date: 2026-09-25 · Status: accepted

## Context

Games arrive as 1..N authorized archive parts and must flow through
download -> verify -> archive extract -> ISO extract -> installer ->
validated install, with disk prechecks and stage-gated cleanup.

## Decision

- Manifest v2 (`schema_version: 2`) carries ordered `sources` plus an
  explicit `package_pipeline` (archive/result/iso/installer) and a
  `cleanup` policy. No ZIP->ISO->EXE assumption is hardcoded.
- Pipeline states (`PLANNED..READY` + `FAILED`) with a transition table
  and JSON persistence (`internal/agent/acquire`); stages are resumable.
- Orchestrate aria2c (resume/retry/concurrency) and 7-Zip (archives/ISO)
  via fixed argv; stdlib HTTP-resume + zip fallbacks keep tests hermetic.
- Mandatory disk gate (`required = download + peak + installed + headroom`)
  with LRU eviction of `/games` only; clean abort with a user-facing
  required/available/recoverable/missing message.
- Installers run path-validated inside staging, fixed argv, timeout,
  exit-code capture, expected-dir verification; cleanup only deletes a
  stage input after the next stage verifies. Saves are never touched.

## Consequences

- No custom downloader/extractor, no DRM or file-host bypass tooling.
- Installer execution is contained: no arbitrary remote shell, closed
  command enum unchanged.
