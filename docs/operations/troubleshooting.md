# Troubleshooting

- `go build ./...` fails: check `go version` (>=1.24), run `gofmt -l .`.
- Control plane exits: `PG_DATABASE_URL` unset (see `.env.example`).
- Agent exits: heartbeat >= lease TTL, or empty control URL.
- Enroll 400: `node_id` required in POST body.
- No GPU reported: honest zero-value when `nvidia-smi` absent; set
  `PG_DISK_*`/`PG_FORCE_*` overrides only for tests.
