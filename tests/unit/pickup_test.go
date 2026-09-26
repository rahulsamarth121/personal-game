package unit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/personal-game/personal-game/internal/agent/backend"
	"github.com/personal-game/personal-game/internal/control/api"
	"github.com/personal-game/personal-game/internal/control/nodes"
	csession "github.com/personal-game/personal-game/internal/control/session"
	"github.com/personal-game/personal-game/pkg/protocol"
)

func TestBackendSelectHonest(t *testing.T) {
	be, avail := backend.Select()
	if avail.Mode == backend.ModeMock {
		t.Fatal("Select must never return MOCK")
	}
	if avail.Mode == backend.ModeReal {
		if be == nil || avail.Reason != "" {
			t.Fatalf("REAL needs backend and empty reason: %+v", avail)
		}
		if be.Probe().Mode != backend.ModeReal {
			t.Fatal("returned backend must probe REAL itself")
		}
	} else if avail.Mode != backend.ModeUnavailable || avail.Reason == "" {
		t.Fatalf("non-REAL must be UNAVAILABLE with reason: %+v", avail)
	}
}

func TestSessionsForNode(t *testing.T) {
	cat := testCatalog(t)
	reg := nodes.New()
	reg.Enroll("n1", capableCaps())
	reg.Enroll("n2", capableCaps())
	mgr := csession.NewManager(csession.NewMemoryStore(), cat, reg)

	s1, err := mgr.Create("u1", "doom2") // lands on freshest node; force to n1 below if needed
	if err != nil {
		t.Fatal(err)
	}
	_ = s1
	// Pin sessions explicitly for determinism.
	mk := func(node string) *protocol.Session {
		n, _ := reg.Enroll(node, capableCaps())
		_ = n
		s := &protocol.Session{
			SchemaVersion: protocol.SessionSchemaVersion, SessionID: csession.NewID(),
			UserID: "u1", GameID: "doom2", NodeID: node,
			FenceToken: n.FenceToken, State: protocol.SessionPreparing,
		}
		if err := mgr.Store.Save(s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	a := mk("n1")
	b := mk("n1")
	_ = b
	_ = mk("n2")

	got, err := mgr.SessionsForNode("n1")
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, s := range got {
		ids[s.SessionID] = true
	}
	if !ids[a.SessionID] || !ids[b.SessionID] || len(got) < 2 {
		t.Fatalf("fresh sessions must be listed: %+v", got)
	}
	// Published stream info means a runner already owns it.
	if _, err := mgr.SetStream(a.SessionID, "n1", a.FenceToken,
		protocol.StreamConfig{Provider: "wolf", Host: "h"}); err != nil {
		t.Fatal(err)
	}
	// STREAMING sessions are live, not pickup work.
	if _, err := mgr.Observe(b.SessionID, "n1", b.FenceToken, protocol.SessionReady, false); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Observe(b.SessionID, "n1", b.FenceToken, protocol.SessionStreaming, true); err != nil {
		t.Fatal(err)
	}
	got, _ = mgr.SessionsForNode("n1")
	ids = map[string]bool{}
	for _, s := range got {
		ids[s.SessionID] = true
	}
	if ids[a.SessionID] || ids[b.SessionID] {
		t.Fatalf("owned/live sessions must not be pickup work: %+v", got)
	}
	// Closed sessions never listed.
	if _, err := mgr.Close(b.SessionID, "n1", b.FenceToken, protocol.EndGameExit); err != nil {
		t.Fatal(err)
	}
	got, _ = mgr.SessionsForNode("n1")
	for _, s := range got {
		if s.State.IsTerminal() {
			t.Fatalf("terminal session listed: %+v", s)
		}
	}
}

func TestNodeSessionsRoute(t *testing.T) {
	cat := testCatalog(t)
	reg := nodes.New()
	reg.Enroll("n1", capableCaps())
	mgr := csession.NewManager(csession.NewMemoryStore(), cat, reg)
	srv := api.NewWithSessions(cat, reg, mgr)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	if _, err := mgr.Create("u1", "doom2"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(ts.URL + "/v1/nodes/n1/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Sessions []protocol.Session `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Sessions) == 0 {
		t.Fatal("assigned session must be visible for pickup")
	}
	resp2, err := http.Get(ts.URL + "/v1/nodes/ghost/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var out2 struct {
		Sessions []protocol.Session `json:"sessions"`
	}
	_ = json.NewDecoder(resp2.Body).Decode(&out2)
	if len(out2.Sessions) != 0 {
		t.Fatal("unknown node must see no work")
	}
}
