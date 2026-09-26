-- 0001_init: foundation schema (Stage 0).
-- PostgreSQL is authoritative for pointers, generations, metadata, ordering.
-- Object storage (R2) holds immutable blobs; it is never the database.

CREATE TABLE IF NOT EXISTS users (
  user_id TEXT PRIMARY KEY,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS games (
  game_id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS game_versions (
  game_id TEXT NOT NULL REFERENCES games(game_id) ON DELETE CASCADE,
  version TEXT NOT NULL,
  manifest JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (game_id, version)
);

CREATE TABLE IF NOT EXISTS nodes (
  node_id TEXT PRIMARY KEY,
  state TEXT NOT NULL DEFAULT 'IDLE',
  health TEXT NOT NULL DEFAULT 'HEALTHY',
  fence_token BIGINT NOT NULL DEFAULT 0,
  last_seen TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS node_capabilities (
  node_id TEXT PRIMARY KEY REFERENCES nodes(node_id) ON DELETE CASCADE,
  caps JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS sessions (
  session_id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(user_id),
  game_id TEXT NOT NULL REFERENCES games(game_id),
  node_id TEXT REFERENCES nodes(node_id),
  fence_token BIGINT NOT NULL,
  state TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  ready_at TIMESTAMPTZ,
  closed_at TIMESTAMPTZ,
  active_seconds BIGINT NOT NULL DEFAULT 0,
  wall_seconds BIGINT NOT NULL DEFAULT 0,
  prepare_seconds BIGINT NOT NULL DEFAULT 0,
  end_reason TEXT,
  stream JSONB
);

CREATE TABLE IF NOT EXISTS session_events (
  id BIGSERIAL PRIMARY KEY,
  session_id TEXT NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
  at TIMESTAMPTZ NOT NULL DEFAULT now(),
  event TEXT NOT NULL,
  detail JSONB
);
CREATE INDEX IF NOT EXISTS idx_session_events_session ON session_events(session_id);

-- One row per (user, game): the authoritative latest pointer.
CREATE TABLE IF NOT EXISTS save_pointers (
  user_id TEXT NOT NULL,
  game_id TEXT NOT NULL,
  latest_generation BIGINT NOT NULL DEFAULT 0,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, game_id)
);

-- One row per immutable generation.
CREATE TABLE IF NOT EXISTS save_snapshots (
  user_id TEXT NOT NULL,
  game_id TEXT NOT NULL,
  generation BIGINT NOT NULL,
  sha256 TEXT NOT NULL,
  size_bytes BIGINT NOT NULL,
  file_count INT NOT NULL DEFAULT 0,
  node_id TEXT,
  session_id TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  state TEXT NOT NULL DEFAULT 'PENDING',
  parent_generation BIGINT,
  PRIMARY KEY (user_id, game_id, generation)
);
CREATE INDEX IF NOT EXISTS idx_save_snapshots_state ON save_snapshots(state);

CREATE TABLE IF NOT EXISTS game_cache_metadata (
  node_id TEXT NOT NULL,
  game_id TEXT NOT NULL,
  version TEXT NOT NULL,
  installed_bytes BIGINT NOT NULL DEFAULT 0,
  last_used TIMESTAMPTZ NOT NULL DEFAULT now(),
  protected BOOLEAN NOT NULL DEFAULT FALSE,
  PRIMARY KEY (node_id, game_id)
);
