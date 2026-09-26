// Package postgres documents the persistence boundary. The real *sql.DB
// wiring lands in Stage 3 with migration 0001; this file keeps the package
// buildable and states the invariant: PostgreSQL is authoritative for
// pointers, generations, metadata, ordering, and session relationships.
package postgres

// Tables (see ../migrations/0001_init.sql):
// users, games, game_versions, nodes, node_capabilities,
// sessions, session_events, save_pointers, save_snapshots, game_cache_metadata.
const DriverName = "postgres"
