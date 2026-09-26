# ADR 0001 — Project architecture (monorepo Go orchestration, reuse streaming)

Date: 2026-09-25 · Status: accepted

## Context

Personal (not commercial multi-user) cloud gaming. Must run LOCAL and on
Kaggle with one agent core. Streaming, encode, NAT traversal are solved
problems (Wolf/Sunshine/Moonlight/Tailscale).

## Decision

- Go monorepo: `cmd/agent`, `cmd/controlplane`, `internal/*`, `pkg/protocol`.
- Our value is orchestration (sessions, saves, cache, history); reuse
  mature streaming/capture/encode/input stacks.
- Provider differences as Capabilities; Kaggle isolated to `kaggle/`.
- PostgreSQL-first, R2 blobs, Tailscale phase 1, fencing mandatory.

## Consequences

- No custom protocol/encoder/NAT in v1; streaming backend pinned in ADR-0003.
- Scheduler matches manifests against capabilities; unknown caps = constrained.
