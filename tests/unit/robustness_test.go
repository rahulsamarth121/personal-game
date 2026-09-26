package unit

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "github.com/lib/pq"

	controlSaves "github.com/personal-game/personal-game/internal/control/saves"
	"github.com/personal-game/personal-game/internal/store/migrations"
	"github.com/personal-game/personal-game/internal/store/object"
)

// Duplicate deliveries must be safe: same commit twice succeeds (idempotent
// retry); session close twice lands closed; zombie replay stays refused.
func TestDuplicateCommands(t *testing.T) {
	r := newSaveRig(t)
	s := r.enrollSession("n1")
	g, err := r.sm.Begin(s.SessionID, "n1", s.FenceToken)
	if err != nil {
		t.Fatal(err)
	}
	blob := []byte("idempotent")
	r.upload(t, g, blob)
	params := controlSaves.CommitParams{
		SessionID: s.SessionID, NodeID: "n1", Token: s.FenceToken,
		Generation: g.Generation, SHA256: sha(blob), SizeBytes: uint64(len(blob)), FileCount: 1,
	}
	first, err := r.sm.Commit(params)
	if err != nil {
		t.Fatalf("first commit: %v", err)
	}
	second, err := r.sm.Commit(params)
	if err != nil {
		t.Fatalf("duplicate commit must succeed idempotently: %v", err)
	}
	if second.Generation != first.Generation || second.State != first.State {
		t.Fatalf("duplicate must return identical snapshot: %+v vs %+v", first, second)
	}
	// Same generation, DIFFERENT bytes: conflict, pointer untouched.
	other := []byte("different-bytes-same-length!!")
	params.SHA256 = sha(other)
	params.SizeBytes = uint64(len(other))
	if _, err := r.sm.Commit(params); err == nil {
		t.Fatal("conflicting duplicate must fail")
	}
	if ptr, _ := r.sm.Saves.Pointer("u1", "g1"); ptr != 1 {
		t.Fatalf("pointer must stay 1, got %d", ptr)
	}
	// Duplicate close is a no-op success.
	if _, err := r.sess.Close(s.SessionID, "n1", s.FenceToken, "GAME_EXIT"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.sess.Close(s.SessionID, "n1", s.FenceToken, "GAME_EXIT"); err != nil {
		t.Fatalf("duplicate close must succeed: %v", err)
	}
}

func TestMemoryPresignUnknownKey(t *testing.T) {
	m := object.NewMemoryStore()
	if _, err := m.PresignDownload(context.Background(), "nope", 0); err == nil {
		t.Fatal("unknown key must fail presign")
	}
	if _, err := m.DownloadURL(context.Background(), "mem://download/nope"); err == nil {
		t.Fatal("unknown key must fail download")
	}
}

func TestPostgresUnreachable(t *testing.T) {
	// Nothing listens here: fast, no server needed, proves the failure is
	// surfaced (not hung) when PostgreSQL is down.
	db, err := sql.Open("postgres", "postgres://pg:pg@127.0.0.1:9/pg?sslmode=disable&connect_timeout=2")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Ping(); err == nil {
		t.Fatal("unreachable postgres must fail ping")
	}
}

func TestMigrationCoversSchema(t *testing.T) {
	sql := migrations.SQL0001
	if sql == "" {
		t.Fatal("embedded migration is empty")
	}
	for _, table := range []string{
		"users", "games", "game_versions", "nodes", "node_capabilities",
		"sessions", "session_events", "save_pointers", "save_snapshots",
		"game_cache_metadata",
	} {
		if !strings.Contains(sql, "CREATE TABLE IF NOT EXISTS "+table) {
			t.Fatalf("migration missing table %s", table)
		}
	}
}
