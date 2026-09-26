# Media networks (how Moonlight reaches the game)

The media path is always native and direct:

```
Moonlight (Windows)  <->  native TCP/UDP  <->  Wolf/Sunshine (GPU node)
```

The control plane never relays video, audio, or input. It only relays the
*endpoint* the node advertises. Which transport carries that traffic is a
node capability, not a code branch.

## Provider abstraction

A node advertises a `MediaNetwork` in its capability report:

| field | meaning |
|---|---|
| `provider` | `tailscale` \| `cloudflare_private_network` \| `direct` |
| `endpoint` | Moonlight-reachable IP or DNS name |
| `address_family` | `ipv4` \| `ipv6` \| `dns` |
| `tcp_ok`, `udp_ok` | path supports what Moonlight needs |
| `reachable` | measured/verified, never assumed |
| `detail` | honest reason when not usable |

Scheduler rule: a node whose media network is not **usable** never receives
a session. Unknown providers are rejected at parse time — there is no
provider-name hack.

## Configuration (node environment)

```bash
MEDIA_NETWORK=tailscale                   # default; advertised only when up
MEDIA_NETWORK=cloudflare_private_network  # Cloudflare One/WARP private routing
MEDIA_NETWORK=direct                      # public IP/DNS you own
MEDIA_ENDPOINT=<ip-or-dns>                # required for cloudflare/direct
STREAMING_ALLOWED=true                    # operator policy: this host may stream
```

`STREAMING_ALLOWED` is an explicit operator declaration. Nodes without it
are skipped by the scheduler regardless of hardware; sessions report
"harness not authorized" instead of silently failing later. Only enable it
where the infrastructure provider permits interactive game streaming.

## Tailscale (default)

Installed/running/peer state is probed at startup (`tailscale status`,
`tailscale ip --4`). The node advertises `tailscale` **only** when the
daemon answers with an address; absence is a diagnostic with remediation,
never faked.

## Cloudflare private network (Option A — preferred Cloudflare path)

Concept:

```
Windows (WARP enrolled) -> Cloudflare private network -> cloudflared
tunnel connector (GPU node) -> Wolf/Sunshine
```

The node keeps a local `cloudflared` connector serving a private network
route; the Windows machine is enrolled in the same Cloudflare Zero Trust
organization with WARP, so the node's private address routes natively
(TCP+UDP) without exposing the game server publicly.

Node configuration:

```bash
MEDIA_NETWORK=cloudflare_private_network
MEDIA_ENDPOINT=172.16.9.9        # the node address inside the WARP network
```

The agent advertises this endpoint only when a local `cloudflared`
connector is present; otherwise it reports exactly what is missing.
Moonlight simply connects to `MEDIA_ENDPOINT` — the Windows client never
proxies anything itself.

Notes: native TCP/UDP routing of this kind is a Cloudflare One capability —
plan-dependent. Spectrum (Option B) is a paid Layer-4 TCP/UDP proxy product;
the free plan does not provide arbitrary UDP Spectrum apps, and this project
does not pretend otherwise. Cloudflare **Workers** are never used as a media
proxy — they cannot terminate game UDP; at most they could serve the control
API, which remains the Go control plane here.

## Direct

For networks you control end to end (LAN or a public IP with only the
streaming ports open). `MEDIA_ENDPOINT` is required; the agent cannot probe
internet reachability from inside, so "reachable" reflects the operator's
explicit configuration.
