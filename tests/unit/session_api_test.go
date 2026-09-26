package unit

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/personal-game/personal-game/internal/control/api"
	"github.com/personal-game/personal-game/internal/control/catalog"
	"github.com/personal-game/personal-game/internal/control/nodes"
	csession "github.com/personal-game/personal-game/internal/control/session"
	"github.com/personal-game/personal-game/pkg/protocol"
)

func testCatalog(t *testing.T) *catalog.Registry {
	t.Helper()
	c := catalog.New()
	if err := c.Put(protocol.GameManifest{
		SchemaVersion: 1, GameID: "doom2", Name: "DOOM II", Version: "1",
		Acquisition: protocol.AcquisitionRef{Provider: "archive"},
		Launch:      protocol.LaunchSpec{Executable: "doom2.exe"},
	}); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestScheduleSkipsUnfit(t *testing.T) {
	now := time.Now()
	good := &nodes.Node{NodeID: "good", State: protocol.NodeIdle, Health: protocol.NodeHealthy, Caps: capableCaps(), LastSeen: now}
	dead := &nodes.Node{NodeID: "dead", State: protocol.NodeIdle, Health: protocol.NodeDead, Caps: capableCaps(), LastSeen: now}
	busy := &nodes.Node{NodeID: "busy", State: protocol.NodeBusy, Health: protocol.NodeHealthy, Caps: capableCaps(), LastSeen: now}
	if got := csession.PickNode([]*nodes.Node{dead, busy}, protocol.CapabilityRequirements{}, now); got != nil {
		t.Fatalf("expected no node, got %s", got.NodeID)
	}
	if got := csession.PickNode([]*nodes.Node{dead, good, busy}, protocol.CapabilityRequirements{}, now); got == nil || got.NodeID != "good" {
		t.Fatalf("expected good, got %+v", got)
	}
	// Gamepad requirement filters gamepad-less nodes.
	plainCaps := capableCaps()
	plainCaps.Gamepad = false
	plainNode := &nodes.Node{NodeID: "plain", State: protocol.NodeIdle, Health: protocol.NodeHealthy, Caps: plainCaps, LastSeen: now}
	padCaps := capableCaps()
	padCaps.Gamepad = true
	pad := &nodes.Node{NodeID: "pad", State: protocol.NodeIdle, Health: protocol.NodeHealthy, Caps: padCaps, LastSeen: now.Add(-time.Minute)}
	if got := csession.PickNode([]*nodes.Node{plainNode, pad},
		protocol.CapabilityRequirements{Gamepad: true}, now); got == nil || got.NodeID != "pad" {
		t.Fatalf("expected pad, got %+v", got)
	}
}

func TestScheduleRequiresStreamingPolicy(t *testing.T) {
	now := time.Now()
	// Capable hardware but the operator did not allow streaming.
	noPolicy := capableCaps()
	noPolicy.StreamingAllowed = false
	n1 := &nodes.Node{NodeID: "n1", State: protocol.NodeIdle, Health: protocol.NodeHealthy, Caps: noPolicy, LastSeen: now}
	if got := csession.PickNode([]*nodes.Node{n1}, protocol.CapabilityRequirements{}, now); got != nil {
		t.Fatalf("streaming_allowed=false node must never receive a session, got %s", got.NodeID)
	}
	// Missing streaming backend.
	noBE := capableCaps()
	noBE.Streaming = nil
	n2 := &nodes.Node{NodeID: "n2", State: protocol.NodeIdle, Health: protocol.NodeHealthy, Caps: noBE, LastSeen: now}
	if got := csession.PickNode([]*nodes.Node{n2}, protocol.CapabilityRequirements{}, now); got != nil {
		t.Fatalf("backend-less node must not be scheduled, got %s", got.NodeID)
	}
	// Unusable media network (endpoint not reachable).
	noMedia := capableCaps()
	noMedia.Network.MediaNetwork.Reachable = false
	n3 := &nodes.Node{NodeID: "n3", State: protocol.NodeIdle, Health: protocol.NodeHealthy, Caps: noMedia, LastSeen: now}
	if got := csession.PickNode([]*nodes.Node{n3}, protocol.CapabilityRequirements{}, now); got != nil {
		t.Fatalf("node without a usable media endpoint must not be scheduled, got %s", got.NodeID)
	}
	// And the honest reason names the policy gap first.
	if reason := csession.UnavailableReason([]*nodes.Node{n1}); reason == "" || !strings.Contains(reason, "STREAMING_ALLOWED") {
		t.Fatalf("unavailable reason should name policy: %q", reason)
	}
}

func TestSchedulePrefersMoreVRAM(t *testing.T) {
	now := time.Now()
	big := capableCaps()
	big.GPU.VRAMMB = 24576
	small := capableCaps()
	small.GPU.VRAMMB = 6144
	smallNode := &nodes.Node{NodeID: "small", State: protocol.NodeIdle, Health: protocol.NodeHealthy, Caps: small, LastSeen: now}
	bigNode := &nodes.Node{NodeID: "big", State: protocol.NodeIdle, Health: protocol.NodeHealthy, Caps: big, LastSeen: now.Add(-time.Second)}
	got := csession.PickNode([]*nodes.Node{smallNode, bigNode}, protocol.CapabilityRequirements{}, now)
	if got == nil || got.NodeID != "big" {
		t.Fatalf("expected the larger-VRAM node, got %+v", got)
	}
}

func TestSessionLifecyclePlaytime(t *testing.T) {
	cat := testCatalog(t)
	reg := nodes.New()
	reg.Enroll("n1", capableCaps())
	mgr := csession.NewManager(csession.NewMemoryStore(), cat, reg)
	now := time.Now()
	mgr.Now = func() time.Time { return now }

	s, err := mgr.Create("u1", "doom2")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if s.State != protocol.SessionPreparing || s.NodeID != "n1" || s.FenceToken == 0 {
		t.Fatalf("bad assignment: %+v", s)
	}
	// Preparing accrues prepare time, never active time.
	now = now.Add(60 * time.Second)
	s, err = mgr.Observe(s.SessionID, "n1", s.FenceToken, "", false)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if s.PrepareSeconds != 60 || s.ActiveSeconds != 0 {
		t.Fatalf("prepare accounting wrong: %+v", s)
	}
	// Move to streaming via legal edges, then active play accrues.
	for _, st := range []protocol.SessionState{protocol.SessionReady, protocol.SessionStreaming} {
		now = now.Add(time.Second)
		s, err = mgr.Observe(s.SessionID, "n1", s.FenceToken, st, true)
		if err != nil {
			t.Fatalf("observe %s: %v", st, err)
		}
	}
	// Six 100s ticks (each under the phantom-hour cap) accrue 600s.
	base := now
	for i := 1; i <= 6; i++ {
		now = base.Add(time.Duration(i) * 100 * time.Second)
		s, err = mgr.Observe(s.SessionID, "n1", s.FenceToken, "", true)
		if err != nil {
			t.Fatalf("observe: %v", err)
		}
	}
	if s.ActiveSeconds != 600 {
		t.Fatalf("active play not accrued across ticks: %+v", s)
	}
	// A single giant jump is capped: no phantom hours after an outage.
	now = base.Add(10 * time.Hour)
	s, err = mgr.Observe(s.SessionID, "n1", s.FenceToken, "", true)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if s.ActiveSeconds != 720 {
		t.Fatalf("giant tick should cap at +120s: %+v", s)
	}
	// Zombie (wrong token) observes nothing and changes nothing.
	if _, err := mgr.Observe(s.SessionID, "n1", 9999, "", true); err == nil {
		t.Fatal("zombie observation must be refused")
	}
	// Zombie cannot close either.
	if _, err := mgr.Close(s.SessionID, "n1", 9999, protocol.EndGameExit); err == nil {
		t.Fatal("zombie close must be refused")
	}
	s, err = mgr.Close(s.SessionID, "n1", s.FenceToken, protocol.EndGameExit)
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if s.State != protocol.SessionClosed || s.ClosedAt == nil || s.EndReason != protocol.EndGameExit {
		t.Fatalf("bad close: %+v", s)
	}
	stats, err := mgr.Playtime("u1")
	if err != nil || len(stats) != 1 || stats[0].Sessions != 1 || stats[0].TotalActiveSec < 600 {
		t.Fatalf("bad playtime: %+v %v", stats, err)
	}
	if stats[0].LastPlayed == nil {
		t.Fatal("last played missing")
	}
}

func TestSessionNoCapableNode(t *testing.T) {
	cat := testCatalog(t)
	reg := nodes.New() // empty: nothing to schedule
	mgr := csession.NewManager(csession.NewMemoryStore(), cat, reg)
	if _, err := mgr.Create("u1", "doom2"); err == nil {
		t.Fatal("create without nodes must fail clearly")
	}
	if _, err := mgr.Create("u1", "nope"); err == nil {
		t.Fatal("create with unknown game must fail")
	}
}

func TestSessionAPIEndToEnd(t *testing.T) {
	cat := testCatalog(t)
	reg := nodes.New()
	reg.Enroll("n1", capableCaps())
	srv := api.New(catalog.New(), reg)
	// Rebuild server against our catalog via session manager wiring:
	mgr := csession.NewManager(csession.NewMemoryStore(), cat, reg)
	srv = api.NewWithSessions(cat, reg, mgr)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	post := func(path string, v any) (int, map[string]any) {
		raw, _ := json.Marshal(v)
		resp, err := http.Post(ts.URL+path, "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	code, body := post("/v1/sessions", map[string]any{"user_id": "u1", "game_id": "doom2"})
	if code != http.StatusCreated || body["session_id"] == nil || body["node_id"] != "n1" {
		t.Fatalf("create session: %d %+v", code, body)
	}
	id := body["session_id"].(string)
	resp, err := http.Get(ts.URL + "/v1/sessions/" + id)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("get session: %d", resp.StatusCode)
	}
	resp, err = http.Get(ts.URL + "/v1/stats/playtime?user_id=u1")
	if err != nil {
		t.Fatal(err)
	}
	var pt map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&pt)
	resp.Body.Close()
	if pt["playtime"] == nil {
		t.Fatalf("playtime missing: %+v", pt)
	}
	// Close requires the live fence token; wrong token -> 409.
	req, _ := http.NewRequest(http.MethodDelete,
		ts.URL+"/v1/sessions/"+id+"?node_id=n1&fence_token=9999&reason=GAME_EXIT", nil)
	bad, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	bad.Body.Close()
	if bad.StatusCode != http.StatusConflict {
		t.Fatalf("zombie close should 409, got %d", bad.StatusCode)
	}
}
