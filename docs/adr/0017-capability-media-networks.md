# ADR 0017 — Capability-driven scheduling and media-network abstraction

Date: 2026-09-26 · Status: accepted

## Context

The scheduler treated any healthy node with capacity as gameable, and the
media path was Tailscale-hardcoded (`host := caps.Network.TailscaleIP`).
Real deployments differ: some hosts are compute-only, some are authorized
for interactive streaming, and the client-to-node media path may ride
Tailscale, a Cloudflare private network (WARP + tunnel), or a direct
connection. Separately, the client API surface needed opt-in bearer auth.

## Decision

- **Streaming is policy, not detection.** Nodes declare
  `STREAMING_ALLOWED=true` in their environment (an operator decision tied
  to what the infrastructure provider permits — Kaggle's AUP, for example,
  forbids game-streaming use, so a Kaggle node never sets it). The bit
  lives in `Capabilities.StreamingAllowed`; `Satisfies` fails without it.
  There are no `if kaggle` branches anywhere.
- **Media network is a capability.** `NetworkCap.MediaNetwork` reports
  `{provider, endpoint, address_family, tcp_ok, udp_ok, reachable, detail}`
  for `tailscale` | `cloudflare_private_network` | `direct`. Providers are
  resolved from `MEDIA_NETWORK`/`MEDIA_ENDPOINT` env; reachability is
  measured (Tailscale daemon probes, cloudflared presence) — never assumed.
  `MediaNetwork.Usable()` gates scheduling. Unknown providers are rejected
  at parse time. The control plane still never touches media packets; it
  relays the endpoint the node published, nothing more.
- **Scheduler is capability-driven** (`PickNode`): policy bit → manifest
  requirements (VRAM/gamepad/HDR) → usable media network → available
  backend → deterministic ranking (more VRAM, fresher lease).
  `UnavailableReason` names the first blocking gap honestly
  (e.g. "nodes are not authorized for game streaming (set
  STREAMING_ALLOWED…)").
- **Auth.** `PG_API_TOKEN` on the control plane enables
  `Authorization: Bearer` checks (constant-time compare) on client routes;
  node enroll/heartbeat/pickup stay authenticated by enrollment credential
  + fence tokens, and `/healthz` stays open. Empty token = open local-dev
  behavior. The Windows UI and `cmd/play` send the token when configured;
  it is never logged or persisted in Git.
- **Cloudflare posture.** Option A (WARP + cloudflared private-network
  routing) is the supported Cloudflare path: the node advertises its
  private-network address as the media endpoint with a local connector
  present; Moonlight dials it natively over TCP/UDP. Workers are never a
  media proxy; Spectrum (paid Layer-4 product) is representable as another
  provider but is not implemented or assumed on free plans. See
  `docs/operations/media-networks.md`.

## Consequences

- Session placement is honest: a node that cannot or may not stream is
  skipped with a reason, before any download starts.
- Adding a new media provider is a new `MediaProvider` constant + env
  resolution — no scheduler or API changes.
- Client auth is one env var on the plane + one settings field in the UI.
- No changes to session state machine, saves/fencing, acquisition, or the
  media path implementations themselves.
