# Local operations: the vertical slice

Prereqs: Go >= 1.24, a Linux GPU host for Wolf (or Sunshine on any OS),
Moonlight on the client, Tailscale for the direct path. See `wolf.md`,
`moonlight.md`, `status.md` for what is REAL vs MOCK on your machine.

## Normal mode (one terminal for the stack)

```bash
python ../local/runner/runner.py up   # control plane + agent together
```

Then play (another shell — the client shell, not infrastructure):

```bash
go run ./cmd/play pair --host <node-ip>   # once per node
go run ./cmd/play --game test-game-local  # session -> READY -> Moonlight auto-launch
```

The agent picks up assigned sessions automatically — no session IDs to
copy. Flow: prepare (warm cache or acquire) -> restore save -> backend
start -> READY -> Moonlight connects -> STREAMING -> exit -> final save ->
close -> playtime. If Wolf/Sunshine is absent the agent refuses the
session with the exact setup remediation — never fake-ready. First run
needs a real game: point `games/manifests/test-game-local.json` at your
legitimate copy (`file://` URL) or add your own manifest (v3, never
binaries in git).

With Docker (Postgres + MinIO + control plane): see `deploy/compose/`.

## Debug/development mode

Run pieces separately when diagnosing:

```bash
go run ./cmd/controlplane          # terminal 1
python ../local/runner/runner.py   # terminal 2 (agent only)
go run ./cmd/play --game <id> --print-only
PG_SESSION_ID=<id> go run ./cmd/agent   # run exactly one session
```

`PG_SESSION_ID` is debug-only; normal use never needs it.

Troubleshooting: `diagnostics` subcommand of the runner is read-only;
`docs/operations/troubleshooting.md` for the rest.
