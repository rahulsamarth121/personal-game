# Personal Game — Cloudflare edge relay (control path only)

A Cloudflare Worker that gives the Windows launcher and Kaggle/game nodes a
stable public HTTPS endpoint in front of the Go control plane:

```
Windows / node  ->  https://<worker>/personal-game/v1/...  ->  Go control plane
```

**Edge transport only.** The Worker is never in the Moonlight media path —
no video, audio, input, or raw game TCP/UDP passes through it. Moonlight
connects to the node's advertised media endpoint directly
(`docs/operations/media-networks.md`).

## Routes

| route | behavior |
|---|---|
| `GET /` | `PERSONAL GAME RELAY OK` (Worker alive) |
| `GET /personal-game/health` | `{"relay":"ok","upstream":"ok|unreachable|...","project":"personal-game"}` |
| `/personal-game/v1/*` | reverse-proxied to `CONTROL_PLANE_ORIGIN` + same path |
| anything else | `404 {"error":"not found"}` |

Path rewrite strips exactly the `/personal-game` prefix:
`/personal-game/v1/games` → `<origin>/v1/games`. Query strings are
preserved. The upstream origin is fixed by configuration — a client can
never choose a destination. Only GET/HEAD/POST/PUT/DELETE/OPTIONS are
forwarded; bodies are capped (10 MB default); `Authorization` is forwarded
opaquely to the Go API (which enforces `PG_API_TOKEN`) and never logged.

## Configure

Set the one required variable (`wrangler.toml [vars]`, or
`wrangler secret put CONTROL_PLANE_ORIGIN` to keep it out of the file):

```toml
[vars]
CONTROL_PLANE_ORIGIN = "https://your-control-plane-hostname"
```

The origin must be reachable from Cloudflare's network: a public HTTPS
hostname (e.g. a Cloudflare Tunnel `cloudflared` hostname in front of the
Go control plane), or a Cloudflare-connected private origin. `127.0.0.1`
on your Windows machine is not remotely reachable — the Worker will report
`upstream: unreachable` rather than pretend.

## Deployed instance

| | |
|---|---|
| Worker name | `personal-game-relay` |
| URL | `https://personal-game-relay.rahul-zed-relay-84739261.workers.dev` |
| Account | authenticated via local `wrangler login` (OAuth; no tokens in git) |

The user's pre-existing `zed-relay-test` Worker serves an unrelated,
authenticated ZED relay. It is **not** this project's Worker and must not
be modified. Deploy only from this directory (`wrangler deploy` binds the
name `personal-game-relay` from `wrangler.toml`).

## Deploy

```bash
cd deploy/cloudflare-worker
npx wrangler login     # once, if needed (browser OAuth; no secrets in git)
npx wrangler deploy
```

## Point clients at it

Windows launcher → Settings → Control Plane URL:

```
https://<worker>.workers.dev/personal-game
```

Kaggle/game node environment:

```
CONTROL_PLANE_URL=https://<worker>.workers.dev/personal-game
```

Trailing slashes are normalized by the clients
(`/personal-game` + `/v1/games` → `/personal-game/v1/games`, never
`//v1/games`). The Go control plane remains the only API authority; the
Worker adds no semantics.

## Tests

```bash
python3 -m pytest worker_test.py
```

Unit tests run the Worker's exported `fetch` against fake upstreams:
health states, path rewriting, query preservation, POST body/JSON
integrity, Authorization forwarding, method rejection, prefix enforcement
(no arbitrary upstream), body cap, and 502 on upstream failure.
