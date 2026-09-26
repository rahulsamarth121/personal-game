# ADR 0014 — Final completion: GPU setup, pairing, journeys, idempotency

Date: 2026-09-25 · Status: accepted

## Context

At ~92% the remaining work was deployment readiness and proof, not
architecture: GPU-node bootstrap, first-time pairing, journey coverage,
and duplicate-delivery safety.

## Decision

- `deploy/gpu-node/setup.py` (single entry, stdlib): `check` diagnostics,
  `install-deps` (official Docker/NVIDIA-toolkit/Tailscale sources),
  `install-wolf` (idempotent pull-or-start with socket mount), `smoke`
  (nvidia-smi, docker+gpus CUDA test, Wolf API, Tailscale); hard-fails on
  missing GPU/driver, `--dry-run` prints without executing.
- Agent reports NVIDIA container acceleration honestly (`nvidia-smi` +
  toolkit/docker-runtime chain; `ContainerRuntime: nvidia` capability).
- `play pair --host [--pin]` delegates to the verified Moonlight CLI;
  pairing stays client-side, never in git.
- E2E grows run3 (JOURNEY E): wiped node → rebuild from source → gen2
  restore → gen3 save. Restore creates missing profile parents (bug found
  by the test, fixed in `swapAll`).
- Duplicate commit of identical bytes is idempotent success (checked
  before ordering validation); conflicting bytes still fail; duplicate
  close is a no-op success.
- R2 `PresignHead` for existence checks without downloads.

## Consequences

- Live GPU/Wolf/Moonlight proof remains BLOCKED BY ENVIRONMENT here;
  every path up to the vendor boundary is implemented and tested.
