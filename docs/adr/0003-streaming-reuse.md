# ADR 0003 — Reuse Wolf/Sunshine/Moonlight; Tailscale first

Date: 2026-09-25 · Status: accepted (backend pin deferred to Stage 1)

## Context

Custom streaming would cost months and lose to mature stacks.

## Decision

Reuse Wolf/Games-on-Whales or Sunshine for capture/encode, Moonlight
(+ moonlight-common-c) for the client, Tailscale for phase-1 networking.
Our surface is auth/library/sessions/saves/cache/history. Evaluate Wolf
vs Sunshine APIs/licensing during Stage 1, then pin one default.

## Consequences

- No custom video/gamepad/NAT code in v1.
- Transport interface keeps direct UDP/ICE/TURN options open (Stage 10).
