# GPU node (Linux + NVIDIA + Docker + Wolf + Tailscale)

One entry point: **`setup.py`** (stdlib only).

```bash
python3 setup.py check                      # diagnostics, safe anywhere
python3 setup.py install-deps [--dry-run]   # Docker, NVIDIA toolkit, Tailscale
python3 setup.py install-wolf [--dry-run]   # pull + run Wolf with node socket
python3 setup.py smoke                      # live validation on the node
python3 setup.py all                        # everything in order
```

Then point the agent at it (`WOLF_SOCKET_PATH=/var/run/wolf/wolf.sock`)
and register the node with the control plane as usual. Details and
remediation per check: run `check` — every failure names its fix.

## Media network + streaming policy (agent environment)

```bash
PG_CONTROL_URL=https://control.example.internal
PG_API_TOKEN=...            # client-API bearer token (control plane side)
NODE_ENROLLMENT_TOKEN=...   # node enrollment credential
NODE_NAME=gpu-1
STREAMING_ALLOWED=true      # ONLY where the infrastructure permits game streaming
MEDIA_NETWORK=tailscale     # or cloudflare_private_network | direct
MEDIA_ENDPOINT=172.16.9.9   # required for cloudflare/direct
PG_SAVE_ROOTS=/srv/game-data  # optional: extra save roots outside the home volume (path-list)
```

`STREAMING_ALLOWED` is an operator policy bit, not a capability lie: the
scheduler skips nodes without it, whatever the hardware. See
`docs/operations/media-networks.md` for the provider model and the
Cloudflare private-network path.

`PG_SAVE_ROOTS` (optional, path-list separated) declares extra directories
the agent may treat as save roots when game/save data legitimately lives
outside the user's home volume — e.g. a portable-save work area on another
drive. It widens the approved set explicitly; it never bypasses validation.
