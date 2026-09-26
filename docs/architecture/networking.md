# Networking: media and control are separate paths

Hard rules (no avoidable Internet delay):

1. The control plane NEVER carries game video, audio, or gameplay input.
2. Streaming uses the direct low-latency transport: client <-> node
   (Moonlight <-> Sunshine/Wolf), ideally over Tailscale in phase 1.
3. Session/control traffic (enroll, heartbeat, begin/commit, presigned
   URLs) rides ordinary HTTPS to the control plane; it is infrequent
   and never in the media path.
4. No video-over-WebSocket relays, no HTTP video tunnels, no extra hops.
5. Relay (TURN-style) only as fallback when direct connectivity fails;
   the transport interface (`internal/agent/transport`, `StreamEndpoint`)
   supports direct-first with relay fallback without redesign.

We do not promise zero physical latency; we eliminate avoidable
software-induced latency. Measure end-to-end (Moonlight stats + session
ready/streaming timestamps) before optimizing (Stage 10).

Phase 1: Tailscale for NAT traversal, identity, encryption. No custom
STUN/ICE/TURN. Cloudflare roles: R2 storage, DNS/Access, optional
TURN/edge proxy — never a video relay.
