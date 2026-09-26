# Kaggle mode (capability-testing node — shares the whole repo)

Single canonical file: **`kaggle/runner.py`** (stdlib only). It clones this
repo, diagnoses capabilities honestly, builds the **shared** Go agent
(`cmd/agent`), and runs it. No second implementation lives here.

> **Kaggle is NOT automatically a gaming host.** Its AUP forbids unrelated
> compute workloads like game streaming. The runner therefore defaults to
> `STREAMING_ALLOWED=false`, and the scheduler will never send a game
> session to it. Only an operator with explicit permission from the
> infrastructure provider should set `STREAMING_ALLOWED=true` — the
> capability system is the mechanism; there is no bypass.

## Run

```bash
python kaggle/runner.py all --repo-url https://github.com/rahulsamarth121/personal-game.git
# stepwise:
python kaggle/runner.py clone | diagnostics | setup | run | cleanup
```

On a completely fresh Kaggle kernel, run notebook cell 1 first
(`kaggle/notebook/personal_game.ipynb`): it loads Secrets, clones the
repository safely, and executes this runner. The repository must exist
before the runner is invoked — it cannot bootstrap itself from nothing.

On startup it prints the node banner and status (never secrets):

```text
PERSONAL GAME NODE
==================
Control Plane:   https://personal-game-relay.rahul-zed-relay-84739261.workers.dev/personal-game
Node runner:     github/kaggle/runner.py
Shared agent:    Go node agent
Streaming policy: capability/configuration controlled
Media:           provider-neutral
```

## Configuration (Kaggle Secrets or env — never committed, never printed)

| variable | purpose |
|---|---|
| `GITHUB_TOKEN` | fine-grained PAT (**secret**) with read access to the private repo — used by `clone` and by the notebook bootstrap; never printed |
| `CONTROL_PLANE_URL` | control-plane base URL. Default: `https://personal-game-relay.rahul-zed-relay-84739261.workers.dev/personal-game` |
| `PG_API_TOKEN` | client-API bearer token (**secret**) if the plane requires one |
| `NODE_ENROLLMENT_TOKEN` | node enrollment credential (**secret**) |
| `NODE_NAME` | node identity (default: Kaggle kernel type) |
| `STREAMING_ALLOWED` | `false` (default) — `true` **only** with explicit provider permission |
| `MEDIA_NETWORK` | `tailscale` \| `cloudflare_private_network` \| `direct` (unset = measured Tailscale) |
| `MEDIA_ENDPOINT` | Moonlight-reachable IP/DNS when the provider needs one |

CLI overrides exist for every key (`--help`), but prefer Secrets over flags
on Kaggle so tokens never appear in notebook output or logs.

## Registration & verification

1. `clone` — fetches the repo into the work root (private repo: set the
   `GITHUB_TOKEN` secret; the token is served via a temporary git askpass
   helper and is never embedded in URLs or printed).
2. `diagnostics` — honest capability report: GPU, encoders, disk, tools,
   control-plane reachability, streaming policy, media network.
3. `setup` — installs/uses the Go toolchain and builds `cmd/agent`.
4. `run` — starts the agent: it enrolls (fence token issued), heartbeats to
   renew its lease, polls for assigned sessions, and reports capabilities.
5. Verify from the control-plane side (Windows UI → Diagnostics, or
   `GET <plane>/v1/nodes/<NODE_NAME>/sessions`).

## Where real gaming happens

Not here. The gaming GPU deployment lives in `deploy/gpu-node/` (Linux +
NVIDIA + Docker + Wolf/Sunshine). Kaggle's role is diagnostics, lifecycle,
and capability testing within its policy — honestly advertised.
