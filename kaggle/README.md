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

## Paste-into-cell mode (Jupyter/Kaggle)

The same file can be pasted **whole** into one notebook cell and Run. The
entry point detects the interactive kernel and never parses the kernel's
own `sys.argv` (it holds a `kernel-*.json` connection file — not runner
input). Direct cell execution runs the safe default `diagnostics` command
(safe and non-destructive: it never clones, starts a game host, or deletes
anything; when a repo is already present it may bootstrap the pinned Go
toolchain into `WORK_ROOT/go` and build/run `agent caps` to report real
capabilities). Later cells
can invoke other commands programmatically with an explicit argument list:

```python
runner.main(["all"])       # clone -> diagnostics -> setup -> run
runner.main(["cleanup"])
```

The kernel's arguments are neither parsed (`parse_known_args` is not used)
nor mutated; argparse validation is unchanged. Outside notebooks, CLI
behavior is identical to before.

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
| `GOROOT_URL` | Go tarball source for the WORK_ROOT bootstrap (default: pinned official `dl.google.com/go/go1.24.11.linux-amd64.tar.gz`) |
| `GOROOT_SHA256` | expected sha256 of that tarball (default: pinned official checksum; `never` disables the bootstrap) |

CLI overrides exist for every key (`--help`), but prefer Secrets over flags
on Kaggle so tokens never appear in notebook output or logs.

## Registration & verification

1. `clone` — fetches the repo into the work root (private repo: set the
   `GITHUB_TOKEN` secret; the token is served via a temporary git askpass
   helper and is never embedded in URLs or printed).
2. `diagnostics` — honest capability report: GPU, encoders, disk, tools,
   control-plane reachability, streaming policy, media network.
3. `setup` — installs/uses the Go toolchain and builds `cmd/agent`
   (bootstrapping it into `WORK_ROOT` when absent — see below).
4. `run` — starts the agent: it enrolls (fence token issued), heartbeats to
   renew its lease, polls for assigned sessions, and reports capabilities.
5. Verify from the control-plane side (Windows UI → Diagnostics, or
   `GET <plane>/v1/nodes/<NODE_NAME>/sessions`).

## Go toolchain bootstrap (no host changes)

Kaggle images ship **no Go toolchain**. The runner does not require one:
when `go` is absent from `PATH`, `setup` (and the agent-capability dump in
`diagnostics`) bootstraps a pinned official toolchain **into
`$WORK_ROOT/go`** — downloaded from `dl.google.com`, verified against a
pinned sha256 *before* extraction, then prepended to the build PATH for
that process only (`GOROOT` set, `GOTOOLCHAIN=local` so Go never
auto-downloads another toolchain). Nothing outside `WORK_ROOT` is touched.

- Order: `go` already on `PATH` wins → reused `$WORK_ROOT/go` wins →
  pinned download. Reruns are idempotent (cached toolchain, cached builds).
- Already-installed Go (self-hosted runners) is preferred and untouched.
- Override either pin via `GOROOT_URL` / `GOROOT_SHA256`; set
  `GOROOT_SHA256=never` to forbid downloads (air-gapped mode, honest
  failure).
- A failing download, checksum mismatch, or corrupted archive is deleted
  and reported — never extracted, never silently ignored.

## Where real gaming happens

Not here. The gaming GPU deployment lives in `deploy/gpu-node/` (Linux +
NVIDIA + Docker + Wolf/Sunshine). Kaggle's role is diagnostics, lifecycle,
and capability testing within its policy — honestly advertised.
