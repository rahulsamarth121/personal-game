# PERSONAL GAME

Personal cloud-gaming launcher/backend (single user, GeForce-NOW-inspired
experience, original implementation).

| piece | what | where |
|---|---|---|
| Windows | PySide6 launcher (Library / Add Game / History / Settings / Diagnostics) | `client/windows/` |
| Control | Go control plane (catalog, sessions, scheduling, saves, playtime) | `cmd/controlplane`, `internal/control/` |
| Node | shared Go node agent (LOCAL + KAGGLE — one implementation) | `cmd/agent` |
| Streaming | Wolf (first) / Sunshine (fallback) on the GPU node | `internal/agent/backend/` |
| Client media | Moonlight (never our code) | docs: `docs/operations/moonlight.md` |
| Control edge | Cloudflare Worker relay — HTTP control only, never media | `deploy/cloudflare-worker/` |
| Persistent saves | PostgreSQL + R2/compatible object storage | `internal/store/`, `deploy/compose/` |
| Local runner | one-terminal local stack | `../local/runner/runner.py` |
| Kaggle runner | capability-testing node (streaming=false by default) | `kaggle/runner.py` |
| Gaming GPU deploy | permitted-host setup | `deploy/gpu-node/` |

Standard remote client URL (Windows Settings → Control Plane URL, or Kaggle
`CONTROL_PLANE_URL`):

```
https://personal-game-relay.rahul-zed-relay-84739261.workers.dev/personal-game
```

## WHAT IT IS

Single-user orchestration layer: library → PLAY → prepare node →
restore → launch → stream → save → history. The GPU node is disposable;
save data is not.

- IS: session/node/save/cache orchestration in Go + reuse of
  Wolf/Sunshine/Moonlight, Ludusavi, aria2c/7-Zip, Tailscale, PG + R2.
- IS NOT: a commercial platform, a custom streaming protocol, billing,
  achievements, DRM/file-host bypass tooling, or a game scraper.

## ARCHITECTURE

```
CONTROL (https, infrequent)          MEDIA (direct, low-latency)
client -> control plane -> node      Moonlight <-> Wolf/Sunshine <-> game
```

- Control plane: auth, catalog, nodes, sessions, saves, playtime.
- Node agent: enroll/heartbeat, acquire, restore/snapshot, backend.
- `GamingBackend`: Wolf (Unix socket) first, Sunshine fallback, Fake MOCK.
- Three storage classes: PERSISTENT (PG + R2) / WARM CACHE (`/games`)
  / EPHEMERAL (downloads, staging). See `docs/architecture/`.

## INSTALL

Requirements: Go >= 1.24, Python >= 3.10 (runners). Optional by role:
`aria2c`, `7z`, `steamcmd`, `ludusavi`, Wolf (Linux GPU host + Docker),
Sunshine, Moonlight, Tailscale, PostgreSQL 16 / Docker Compose.

```bash
go build ./...
go test ./...
```

## LOCAL SETUP

One terminal does everything:

```bash
python ../local/runner/runner.py up     # control plane + agent
# or agent only:  python ../local/runner/runner.py
# single session: go run ./cmd/play --game <id>
```

With Docker (Postgres + MinIO + control plane): see `deploy/compose/`.
Full guide: `docs/operations/local.md`, `wolf.md`, `moonlight.md`.

## GPU NODE

```bash
python3 deploy/gpu-node/setup.py check
python3 deploy/gpu-node/setup.py all   # Ubuntu/Debian NVIDIA host
```

Detects distro/GPU/driver/Docker/NVIDIA toolkit/Tailscale/devices/Wolf,
installs from official sources, runs Wolf with the node socket, and smoke
tests the chain. Hard-fails on missing GPU/driver. Node config is
environment-only (`CONTROL_PLANE_URL`, `NODE_NAME`, `NODE_ENROLLMENT_TOKEN`,
`CACHE_ROOT`, `PG_GAME_ROOT`, `PG_TEMP_ROOT`, `PG_GPU_INDEX`) — never secrets
in git, never source edits for ordinary setup.

## KAGGLE SETUP

```bash
python kaggle/runner.py all --repo-url <url>
# stepwise: clone | diagnostics | setup | run | cleanup
```

Kaggle is diagnostics/lifecycle/capability testing — no gaming claims
beyond real capabilities. See `docs/operations/kaggle.md`.

## ADDING A GAME

No dashboard, no binaries in git — one command writes a valid v3 manifest
into `games/manifests/` (control plane seeds it on boot):

```bash
go run ./cmd/play add-game --game-id doom2 --name "DOOM II" \
  --package-type archive_prebuilt \
  --url https://example.invalid/game.zip --exe game.exe
```

Multipart: repeat `--url` (any N) with optional `--filename/--sha256/--size`
paired by index. Installer modes need `--installer` (+`--iso-result`
`--iso-extract` for ISO). Prebuilt takes `--expected-file` repeats and
refuses `--installer`. `--force` overwrites; validation runs before write.

The Windows UI (`client/windows/`, PySide6) is a guided alternative:
paste one URL per line (1..N parts), pick Prebuilt/Installer (+ISO), and it
builds the same v3 manifest, submits `POST /v1/games`, and the control
plane persists it into the seed directory (`persisted: true` in the
response; see ADR-0016).

## MULTIPART GAME FLOW

```
1..N links → download (resume/retry) → verify → extract → ISO
  → DELETE parts/temp → extract ISO → DELETE ISO
  → installer EXE → install → validate → DELETE installer
  → game in cache → launch → Wolf/Sunshine → Moonlight → PLAY
```

Cases: N=1,2,many parts; tampered parts rejected; order from part
numbers; cleanup only after the next stage verifies.
(`games/examples/case1-*.json`, `case5-*.json`.)

## SINGLE PREBUILT FLOW

```
1 link → download → verify → extract if archived → validate launch EXE
  → DELETE source archive → game in cache → PLAY (no installer ever)
```

## SINGLE INSTALLER FLOW

```
1 link → archive → installer → install → validate → PLAY
```

## DIRECT EXE FLOW

```
1 link to the EXE → download → verify → place → validate → PLAY
```

The EXE is the game, not an installer: installer stages never run for
`direct_prebuilt` (validation rejects `--installer` there).

## PLAYING

```bash
go run ./cmd/play library
go run ./cmd/play pair --host <node-ip>     # once per node
go run ./cmd/play --game <id>               # auto-launches Moonlight
```

Or use the Windows launcher (same flow, no terminal):

```bash
python -m pip install -r client/windows/requirements.txt
python client/windows/main.py                # --control <url> to override
```

The UI wraps the same `cmd/play` session flow (create → assign → prepare →
READY → Moonlight → close), shows staged progress from real session states,
and reports missing Moonlight/node/control plane honestly. For a packaged
app, `python scripts/build_play_client.py` builds `play-client.exe` so
users never install Go.

Waits for assignment → preparation → READY, auto-launches Moonlight when
installed, closes the session on exit, prints playtime. The node agent
picks up sessions automatically (`PG_SESSION_ID` is debug-only).

## USER JOURNEYS

- A (multipart): `add-game` with N `--url` + `--installer` (+`--iso-result`
  `--iso-extract` for ISO) → prepare extracts, deletes parts, extracts
  ISO, deletes ISO, installs, validates, deletes installer → PLAY.
- B (prebuilt): `add-game` with one `--url` + `--exe` (+`--expected-file`)
  → extract, validate, delete source → PLAY, never an installer.
- C (direct EXE): one `--url` to the EXE → place, validate → PLAY.
- D (cached): PLAY again → warm hit, no download (e2e-verified).
- E (new node): destroy node → fresh agent → game rebuilds from source,
  previous save restores, CONTINUE (e2e-verified end to end).

## SAVES

Restore-before-launch, snapshot-after-exit (quiescence-guarded), immutable
generations, fence-checked commits, zombie-proof latest pointer.
Ludusavi CLI when present (`--ludusavi-title`), manifest overrides
otherwise, honest skip when neither. Never stored in game cache, never
evicted. See `docs/architecture/storage.md`.

## HISTORY

```bash
go run ./cmd/play history [--user player1]
```

Game, last played, total active seconds, session count — all server
observed, never client-reported.

## CACHE

`/games/` holds installed/prebuilt games (`state/cache.json` index):
warm hit → validate → use; miss → acquire; LRU eviction with active-game
protection when disk is short (measured bytes, saves never touched).

## NETWORKING

Media NEVER crosses the control plane:

```
CONTROL: client → control plane → node agent (sessions, saves, URLs)
MEDIA:   Moonlight ↔ Wolf/Sunshine ↔ game (native TCP/UDP, provider-neutral)
```

The media transport is a node capability, not a hardcode:
`MEDIA_NETWORK=tailscale | cloudflare_private_network | direct` (see
`docs/operations/media-networks.md`). For control traffic from anywhere,
`deploy/cloudflare-worker/` provides an edge relay
(`docs/operations/cloudflare-worker.md`) — HTTP control calls only, never
media. Nodes only stream when the operator
sets `STREAMING_ALLOWED=true` — a policy bit the scheduler enforces, so a
host that may not stream (e.g. under Kaggle's AUP) never receives a
session. `PG_API_TOKEN` on the control plane turns on bearer auth for
client routes; the Windows UI and `cmd/play` send it when configured.

Tailscale install: `curl -fsSL https://tailscale.com/install.sh | sh`,
then `tailscale up`; verify with `tailscale ip -4` / `tailscale status`.
The agent logs installed/running/IP/peers at startup and refuses to
pretend direct connectivity exists.

## TROUBLESHOOTING

- `no gaming backend`: install Wolf (Linux+DOCKER, `docs/operations/wolf.md`)
  or Sunshine; the agent names exactly what is missing.
- `moonlight not found`: install Moonlight; use `--print-only` meanwhile.
- `tailscale ...`: install/run `tailscale up` for the direct path.
- `insufficient disk: required/available/recoverable/missing`: free space
  or let LRU evict; never starts doomed downloads.
- Session stuck PREPARING: is the node agent running? (`up` mode runs it.)
- More: `docs/operations/troubleshooting.md`, `docs/operations/status.md`
  (REAL vs MOCK vs UNAVAILABLE).

## Status

- Stages 0–3, one-click play, acquisition modes, saves+fencing, backends,
  catalog seeding, providers (archive/file/steamcmd), cache, runners. ✅
- Vertical slice + e2e (ADR-0011), one-click (ADR-0012), finishes (ADR-0013). ✅
- Final completion (ADR-0014): GPU setup.py, NVIDIA runtime capability,
  Moonlight pairing, JOURNEY E rebuild e2e, duplicate idempotency. ✅
- Windows desktop UI (ADR-0016): PySide6 thin client, durable game
  onboarding via POST /v1/games, play via the existing Go client. ✅
- Capability scheduling + media networks (ADR-0017): streaming_allowed
  policy, provider-neutral media endpoints (Tailscale / Cloudflare private
  network / direct), opt-in client-API bearer auth, UI diagnostics. ✅
- Cloudflare edge relay for the control path (live-deployed, tested
  end to end); `zed-relay-test` untouched. ✅

## Next

Live Wolf/Sunshine + GPU deploy verification, more providers, polish.

## Security notes

Closed command enum, path allowlists, short-lived storage URLs, fencing
against zombies. See `docs/architecture/security.md`.

## CURRENT LIMITATIONS

- No live GPU/Wolf/Sunshine/Moonlight on the dev box: those paths are
  implemented + mock-tested, live proof needs the GPU host (`status.md`).
- Multipart reconstruction at N>1 needs 7-Zip at runtime.
- Provider installs need vendor login/entitlements (SteamCMD plumbed).
- PostgreSQL/R2 need a Docker host or cloud project (compose ready).
- Single-user by design: no accounts, billing, or matchmaking — ever.
