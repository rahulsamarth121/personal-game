# Cloudflare Worker — control-plane edge relay

## Purpose

A Cloudflare Worker (`deploy/cloudflare-worker/`) gives the Windows launcher
and Kaggle/game nodes a stable public HTTPS endpoint in front of the Go
control plane:

```
Windows client  ─┐
                 ├─> https://<worker>/personal-game/v1/...  ─>  Go control plane
Kaggle/game node ─┘
```

**Control path only.** The Worker relays ordinary HTTP API calls. It is
never in the Moonlight media path: no video, audio, keyboard, mouse,
gamepad, or raw game TCP/UDP touches it. Media stays native and direct
(`docs/operations/media-networks.md`).

The Go control plane API remains authoritative; the Worker adds no API
semantics. It is a fixed-origin reverse proxy with a strict allowlist.

## Deployed instance

| | |
|---|---|
| Worker name | `personal-game-relay` |
| URL | `https://personal-game-relay.rahul-zed-relay-84739261.workers.dev` |
| Namespace | `/personal-game/*` |
| Worker password | none (development mode) |

The user's pre-existing `zed-relay-test` Worker serves an unrelated,
authenticated ZED relay. It was **not** modified — this is a separate
Worker in the same account. Its behavior was re-verified after deployment
(root still `ZED KAGGLE CONNECTOR OK`, other paths still 401).

## Routes

| route | behavior |
|---|---|
| `GET /` | `PERSONAL GAME RELAY OK` — Worker alive (no upstream involved) |
| `GET /personal-game/health` | `{"relay":"ok","upstream":"ok"\|"unreachable"\|...,"project":"personal-game"}` — probes `<origin>/healthz` honestly |
| `/personal-game/v1/*` | reverse-proxied to `CONTROL_PLANE_ORIGIN` + same path (`/personal-game` stripped, query preserved) |
| anything else | `404 {"error":"not found"}` |
| methods outside GET/HEAD/POST/PUT/DELETE/OPTIONS | `405` (CONNECT/TRACE refused) |

Three distinct states, never conflated:

- **Worker alive** — `GET /` answers.
- **Control plane reachable** — `/personal-game/health` shows `upstream: ok`.
- **Go API authenticated** — an `Authorization: Bearer` header is forwarded
  to the plane, which enforces `PG_API_TOKEN` itself. The Worker neither
  replaces nor logs that layer.

## Hardening (even without a password)

- Exactly one configured upstream: `CONTROL_PLANE_ORIGIN`. Clients cannot
  influence the destination; path-looking payloads stay paths.
- Only `/personal-game/*` is served; everything else 404s.
- Method allowlist; CONNECT/TRACE → 405.
- Request bodies capped (default 10 MB, `RELAY_MAX_BODY_BYTES`).
- Upstream redirects are refused (`redirect: "manual"` + 502), so the relay
  can never be bounced to another origin.
- 30 s upstream timeout → honest `502 {"error":"control plane unavailable"}`.
- Header allowlist (Content-Type, Accept, Authorization, User-Agent);
  no Worker internals exposed; `Authorization` never logged or echoed.

## Configuration & deployment

```bash
cd deploy/cloudflare-worker
# set the real upstream in wrangler.toml [vars], or keep it out of git:
npx wrangler secret put CONTROL_PLANE_ORIGIN   # e.g. https://control.example.com
npx wrangler deploy
```

The upstream must be reachable **from Cloudflare's edge**: a public HTTPS
hostname (e.g. `cloudflared` tunnel hostname in front of the Go control
plane) or a Cloudflare-connected private origin. `127.0.0.1` on your own
machine is not remotely reachable — the Worker reports
`upstream: unreachable` rather than pretending.

Live-tested topology:

```
Windows/nodes -> Worker -> cloudflared tunnel (quick tunnel) -> Go control plane
```

## Client configuration

Windows launcher → Settings → Control Plane URL:

```
https://personal-game-relay.rahul-zed-relay-84739261.workers.dev/personal-game
```

Kaggle/game node environment:

```
CONTROL_PLANE_URL=https://personal-game-relay.rahul-zed-relay-84739261.workers.dev/personal-game
```

Trailing slashes are normalized by the clients (`/personal-game` +
`/v1/games` → `/personal-game/v1/games`, never `//v1/games`), and the
Worker collapses any duplicated slashes again as defense in depth.

## Tests

```bash
cd deploy/cloudflare-worker
python3 -m pytest worker_test.py      # 16 tests, hermetic (Node shim)
```

Covered: root health, relay health (ok / unreachable / misconfigured),
path rewriting, query-string preservation, POST body/JSON integrity,
Authorization forwarding (and never echoing), method rejection incl.
CONNECT, unknown-path rejection, no arbitrary upstream, body cap, 502 on
upstream failure, trailing-slash normalization, no logging of secrets.

## What this does NOT prove

A successful Worker round-trip proves **client ↔ control plane** and
**node ↔ control plane** connectivity only. It says nothing about
Moonlight ↔ GPU streaming, which still requires a permitted gaming node
(`STREAMING_ALLOWED=true`), a real backend (Wolf/Sunshine), and native
TCP/UDP media connectivity via the node's advertised media network. The
Worker is never part of that path — now or later.

## Future media option (not this Worker)

The Cloudflare **media** path remains Option A from
`media-networks.md`: Moonlight → WARP → Cloudflare private network → GPU
host → Wolf/Sunshine. That is native TCP/UDP routing at the network layer,
not a Worker feature. No code here pretends otherwise.
