# REAL vs MOCK vs UNAVAILABLE

Every gaming-path component reports its mode. Production never substitutes
a fake; tests name their fakes explicitly.

## Legend

- DONE: implementation complete.
- LIVE VERIFIED: tested against actual external software/service.
- MOCK VERIFIED: fake backend/test fixture.
- UNAVAILABLE: dependency absent (fails with remediation, never fake-ready).
- BLOCKED BY ENVIRONMENT: live proof needs hardware/services not present
  here (GPU host, vendor binaries, cloud credentials); code is complete.

| Component | Status | Evidence |
|---|---|---|
| Wolf API client | DONE, MOCK VERIFIED | Fake Unix-socket server tests; real socket dial + verified endpoints |
| WolfBackend | DONE, UNAVAILABLE here | Probe/Start/Status/Stop real; no Wolf on this box |
| SunshineBackend | DONE, UNAVAILABLE here | Real supervised processes; no binary on this box |
| FakeBackend | DONE, MOCK VERIFIED | Explicit MOCK, e2e only |
| Moonlight launch | DONE, UNAVAILABLE here | Verified CLI args; auto-launch when binary present |
| Acquisition (aria2c/HTTP/file) | DONE, LIVE VERIFIED | Real aria2c binary test (loopback), resume, file:// |
| 7-Zip/ISO/multipart-extract | DONE, UNAVAILABLE here | Honest error without 7z; zip path LIVE VERIFIED |
| SteamCMD provider | DONE, UNAVAILABLE here | Arg building + failure paths tested; needs binary+login |
| Saves (gen/fence/blob) | DONE, MOCK VERIFIED | Memory + HTTP-loopback stores, chaos regression |
| R2 presigning | DONE, MOCK VERIFIED | RFC 4231 vector, structure tests; live run needs creds |
| PostgreSQL stores | DONE, UNAVAILABLE here | Real SQL, interface-asserted; gated tests skip w/o DB |
| MinIO/R2 live | Compose ready, UNAVAILABLE here | No Docker daemon on this box |
| Ludusavi | DONE, UNAVAILABLE here | Verified CLI integration; negative paths tested |
| Tailscale path | DONE, UNAVAILABLE here | Detail reporting tested; diagnostics wired |
| Cache + eviction | DONE, MOCK VERIFIED | Registry, LRU victims, measured-byte eviction tested |
| Session/playtime | DONE, MOCK VERIFIED | E2E ×3 runs, server-observed |
| Catalog seeding | DONE, LIVE VERIFIED | Control plane boots with `test-game-local` |
| GPU setup | DONE, LIVE VERIFIED | `check` ran live here (GPU found, gaps named); installs need a Linux GPU host |
| Pairing | DONE, UNAVAILABLE here | Verified Moonlight CLI delegation; needs binary |
| Journeys A–E | DONE, MOCK VERIFIED | A–D unit/e2e; E full new-node rebuild e2e |
| Runners | DONE, LIVE VERIFIED | Both compile, help, diagnostics run on this box |
| Windows UI (PySide6) | DONE, MOCK VERIFIED | 65 offscreen pytest tests; API round-trip + 20-part manifest persisted via live control plane; visual smoke render |
| Durable game onboarding | DONE, LIVE VERIFIED | POST /v1/games → seed-dir file → restart re-seed test (Go unit + live curl) |
| Media networks | DONE, MOCK VERIFIED | tailscale/cloudflare/direct resolution + Usable() gating tested; live Tailscale needs a node |
| Streaming policy | DONE, MOCK VERIFIED | streaming_allowed gate tested in scheduler + session creation |
| Client API auth | DONE, MOCK VERIFIED | Bearer middleware (constant-time) tested incl. node-route exemptions |
| Cloudflare edge relay | DONE, LIVE VERIFIED | control path Windows/Worker/tunnel->Go live-tested; unrelated zed-relay-test preserved |

Live-on-this-machine status: Wolf MISSING, Sunshine MISSING, Moonlight
MISSING, Ludusavi MISSING, 7z MISSING, Tailscale MISSING, SteamCMD
MISSING, Postgres MISSING, MinIO MISSING, Docker client PRESENT (daemon
down); Go PRESENT, aria2c PRESENT (live-tested), git PRESENT.
