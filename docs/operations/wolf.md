# Wolf setup (Linux GPU node; Docker)

Wolf runs as a container and streams to Moonlight. Our agent talks to it
over its local Unix socket only — never over the network.

## Install

Follow the official quickstart for your GPU (Intel/AMD, Nvidia Toolkit,
or manual): https://games-on-whales.github.io/wolf/stable/user/quickstart.html

Minimal shape (Nvidia example):

```bash
docker run --name wolf --network=host \
  -v /etc/wolf:/etc/wolf:rw \
  -v /var/run/docker.sock:/var/run/docker.sock:rw \
  -e WOLF_SOCKET_PATH=/var/run/wolf/wolf.sock \
  -v /var/run/wolf:/var/run/wolf \
  --device /dev/dri --device /dev/uinput --device /dev/uhid \
  ghcr.io/games-on-whales/wolf:stable
```

The two socket lines are what our agent needs: Wolf creates
`/var/run/wolf/wolf.sock` on the host, and the agent uses it directly.

## Expose the game to Moonlight

Add one app per game to Wolf's `config.toml` (existing images preferred —
Steam, desktops, or a container that mounts `/games/<id>`; do NOT build a
custom image per game without need). Note the app **title**, then set it in
the manifest:

```json
"launch": { "executable": "game.exe", "wolf_app": "My Game" }
```

Without `wolf_app` the agent falls back to the manifest game name and says
so; a missing app fails with this remediation (never fake-ready).

## Pair Moonlight

In Moonlight add the node (Tailscale IP), read the PIN, and approve it.
To approve pending PINs programmatically over the socket:

```bash
curl --unix-socket /var/run/wolf/wolf.sock http://localhost/api/v1/pair/pending
curl --unix-socket /var/run/wolf/wolf.sock -X POST http://localhost/api/v1/pair/client \
  -d '{"pair_secret":"...","pin":"1234"}'
```

## Verify

```bash
curl --unix-socket /var/run/wolf/wolf.sock http://localhost/api/v1/apps
curl --unix-socket /var/run/wolf/wolf.sock http://localhost/api/v1/sessions
```

The agent checks the same endpoints (`Probe`): socket missing or API
failing means UNAVAILABLE with the exact reason. Never expose the socket
over TCP without authentication — Wolf's own docs call that dangerous.

## Troubleshooting

- `socket not found`: Wolf not running, or `WOLF_SOCKET_PATH` unset in
  the Wolf container (socket path must match the agent's).
- `app "X" not configured`: title mismatch with `config.toml` profiles.
- `no Moonlight client paired`: add + approve the client first.
- Wolf on Windows/macOS: not native — use a Linux GPU host or VM.
