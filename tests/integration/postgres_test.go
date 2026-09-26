// Live PostgreSQL test: runs only when PG_DATABASE_URL is set
// (e.g. deploy/compose). CI without a database skips safely.
package integration

import (
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"

	controlSaves "github.com/personal-game/personal-game/internal/control/saves"
	controlSession "github.com/personal-game/personal-game/internal/control/session"
	"github.com/personal-game/personal-game/internal/store/postgres"
	"github.com/personal-game/personal-game/pkg/protocol"
)

func liveDB(t *testing.T) *sql.DB {
	t.Helper()
	url := os.Getenv("PG_DATABASE_URL")
	if url == "" {
		t.Skip("PG_DATABASE_URL unset: live postgres test skipped")
	}
	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Ping(); err != nil {
		t.Skipf("postgres unreachable, skipping: %v", err)
	}
	if err := postgres.Migrate(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestLivePostgresSessions(t *testing.T) {
	db := liveDB(t)
	store := postgres.NewSessionStore(db)
	s := &protocol.Session{
		SchemaVersion: protocol.SessionSchemaVersion,
		SessionID:     "live-" + time.Now().Format("150405.000000"),
		UserID:        "live-u", GameID: "live-g", NodeID: "live-n",
		FenceToken: 7, State: protocol.SessionPreparing, CreatedAt: time.Now().UTC(),
	}
	if err := store.Save(s); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := store.Get(s.SessionID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.FenceToken != 7 || got.State != protocol.SessionPreparing {
		t.Fatalf("round-trip wrong: %+v", got)
	}
	s.State = protocol.SessionStreaming
	s.ActiveSeconds = 42
	if err := store.Save(s); err != nil {
		t.Fatalf("update: %v", err)
	}
	list, err := store.List("live-u")
	if err != nil || len(list) == 0 {
		t.Fatalf("list: %+v %v", list, err)
	}
	if err := store.AppendEvent(s.SessionID, "test", map[string]any{"k": "v"}); err != nil {
		t.Fatalf("event: %v", err)
	}
	var _ controlSession.Store = store
}

func TestLivePostgresSaves(t *testing.T) {
	db := liveDB(t)
	store := postgres.NewSaveStore(db)
	user, game := "live-u", "live-g"
	snap, err := store.IssuePending(user, game, "n", "s", nil)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if snap.Generation == 0 || snap.State != protocol.SavePending {
		t.Fatalf("bad pending: %+v", snap)
	}
	sha := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	fin, err := store.Finalize(user, game, snap.Generation, protocol.SaveValid, sha, 10, 1)
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if fin.State != protocol.SaveValid || fin.SHA256 != sha {
		t.Fatalf("bad finalize: %+v", fin)
	}
	if err := store.Advance(user, game, snap.Generation); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if err := store.Advance(user, game, snap.Generation); err == nil {
		t.Fatal("backward/duplicate advance must be refused")
	}
	latest, err := store.Latest(user, game)
	if err != nil || latest.Generation != snap.Generation {
		t.Fatalf("latest: %+v %v", latest, err)
	}
	hist, err := store.History(user, game, 5)
	if err != nil || len(hist) == 0 {
		t.Fatalf("history: %+v %v", hist, err)
	}
	var _ controlSaves.Store = store
}
