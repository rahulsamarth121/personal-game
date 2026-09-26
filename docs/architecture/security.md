# Security

Least privilege throughout. Closed command enum
(ACQUIRE/RESTORE/LAUNCH/SNAPSHOT/TERMINATE/EVICT) — no arbitrary EXEC.
Validate every path against allowed roots (see `internal/common/paths.go`).
Short-lived scoped storage URLs; no secrets in git; `.env` never blindly
overwritten. Wolf admin API never exposed to the Internet. Fencing tokens
make zombie nodes harmless: stale holders cannot commit saves, advance
pointers, or start sessions; re-enrollment fences superseded sessions.

Game executables: manifest distinguishes installer EXE from game
executable. Installers run path-validated inside staging, fixed argv
(no shell), enforced timeout, exit-code capture, expected-dir proof —
only then is staging deleted. Prebuilt EXEs are validated, never
executed, by preparation. Save blobs are checksummed; zip-slip and
root-escape rejected at restore. No DRM/file-host bypass tooling.
