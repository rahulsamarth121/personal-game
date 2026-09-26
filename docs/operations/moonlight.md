# Moonlight setup (client)

Moonlight is the ONLY video/audio/input path. Install the official client
for your platform: https://moonlight-stream.org/ (no fork needed).

## Pair (once per node)

```bash
go run ./cmd/play pair --host <node-ip> [--pin <4-digit-PIN>]
# or directly: moonlight pair <node-ip> --pin <4-digit-PIN>
```

Approve the PIN on the Wolf host (see `wolf.md`), or enter it in the
Sunshine web UI for Sunshine backends. Pairing is stored client-side by
Moonlight and reused for all later sessions — never pair every time, and
never commit pairing state to git.

## Play (every session)

Normal mode auto-launches Moonlight when the session is READY:

```bash
go run ./cmd/play --game <id>
```

Debug mode prints the exact verified invocation instead of launching:

```bash
go run ./cmd/play --game <id> --print-only
# moonlight stream <node-ip> "<app>"
```

```bash
moonlight list <node-ip>   # apps the node offers
moonlight quit <node-ip>   # quit the running app
```

## Troubleshooting

- `has not been paired`: run `moonlight pair` first.
- No apps listed: Wolf has no apps configured, or Sunshine exposes none.
- Stutter/latency: prefer same-LAN or Tailscale direct path; Moonlight's
  performance overlay (`--performance-overlay`) measures, don't guess.
