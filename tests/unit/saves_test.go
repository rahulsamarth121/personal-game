package unit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/personal-game/personal-game/internal/control/api"
	"github.com/personal-game/personal-game/internal/control/catalog"
	"github.com/personal-game/personal-game/internal/control/nodes"
	controlSaves "github.com/personal-game/personal-game/internal/control/saves"
	csession "github.com/personal-game/personal-game/internal/control/session"
	"github.com/personal-game/personal-game/internal/store/object"
	"github.com/personal-game/personal-game/pkg/protocol"
)

type saveRig struct {
	t    *testing.T
	cat  *catalog.Registry
	reg  *nodes.Registry
	sess *csession.Manager
	sm   *controlSaves.Manager
	objs *object.MemoryStore
}

func newSaveRig(t *testing.T) *saveRig {
	t.Helper()
	cat := catalog.New()
	if err := cat.Put(protocol.GameManifest{
		SchemaVersion: 1, GameID: "g1", Name: "G1", Version: "1",
		Acquisition: protocol.AcquisitionRef{Provider: "archive"},
		Launch:      protocol.LaunchSpec{Executable: "g.exe"},
	}); err != nil {
		t.Fatal(err)
	}
	reg := nodes.New()
	sess := csession.NewManager(csession.NewMemoryStore(), cat, reg)
	objs := object.NewMemoryStore()
	sm := controlSaves.NewManager(controlSaves.NewMemoryStore(), objs, sess)
	sm.OnCommitted = sess.NoteSaveGen
	return &saveRig{t, cat, reg, sess, sm, objs}
}

func (r *saveRig) enrollSession(node string) *protocol.Session {
	r.t.Helper()
	r.reg.Enroll(node, capableCaps())
	s, err := r.sess.Create("u1", "g1")
	if err != nil {
		r.t.Fatalf("create: %v", err)
	}
	return s
}

func sha(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// upload simulates the node PUTing the blob to its scoped URL.
func (r *saveRig) upload(t *testing.T, grant controlSaves.BeginGrant, blob []byte) {
	t.Helper()
	if err := r.objs.UploadURL(context.Background(), grant.UploadURL, bytes.NewReader(blob), sha(blob)); err != nil {
		t.Fatalf("upload: %v", err)
	}
}

func (r *saveRig) commit(t *testing.T, s *protocol.Session, gen uint64, blob []byte, checkpoint bool) *protocol.SaveSnapshot {
	t.Helper()
	snap, err := r.sm.Commit(controlSaves.CommitParams{
		SessionID: s.SessionID, NodeID: s.NodeID, Token: s.FenceToken,
		Generation: gen, SHA256: sha(blob), SizeBytes: uint64(len(blob)),
		FileCount: 3, Checkpoint: checkpoint,
	})
	if err != nil {
		t.Fatalf("commit gen %d: %v", gen, err)
	}
	return snap
}

func TestSaveFirstAndSecond(t *testing.T) {
	r := newSaveRig(t)
	s := r.enrollSession("n1")

	g1, err := r.sm.Begin(s.SessionID, "n1", s.FenceToken)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if g1.Generation != 1 || g1.UploadURL == "" {
		t.Fatalf("bad grant: %+v", g1)
	}
	blob1 := []byte("save-data-gen-1")
	r.upload(t, g1, blob1)
	snap := r.commit(t, s, g1.Generation, blob1, false)
	if snap.State != protocol.SaveValid || snap.ParentGeneration != nil {
		t.Fatalf("bad snapshot: %+v", snap)
	}
	if ptr, _ := r.sm.Saves.Pointer("u1", "g1"); ptr != 1 {
		t.Fatalf("pointer should be 1, got %d", ptr)
	}

	g2, err := r.sm.Begin(s.SessionID, "n1", s.FenceToken)
	if err != nil {
		t.Fatalf("begin 2: %v", err)
	}
	if g2.Generation != 2 {
		t.Fatalf("expected gen 2, got %d", g2.Generation)
	}
	blob2 := []byte("save-data-gen-2")
	r.upload(t, g2, blob2)
	snap2 := r.commit(t, s, g2.Generation, blob2, true)
	if snap2.State != protocol.SaveCheckpoint {
		t.Fatalf("checkpoint should mark CHECKPOINT: %+v", snap2)
	}
	if snap2.ParentGeneration == nil || *snap2.ParentGeneration != 1 {
		t.Fatalf("parent should be 1: %+v", snap2)
	}
	// Session history linkage.
	got, _ := r.sess.Store.Get(s.SessionID)
	if len(got.SaveGens) != 2 || got.SaveGens[0] != 1 || got.SaveGens[1] != 2 {
		t.Fatalf("session save gens wrong: %+v", got.SaveGens)
	}
}

func TestSaveRestoreLatest(t *testing.T) {
	r := newSaveRig(t)
	s := r.enrollSession("n1")
	for i, data := range []string{"v1", "v2-data"} {
		g, err := r.sm.Begin(s.SessionID, "n1", s.FenceToken)
		if err != nil {
			t.Fatal(err)
		}
		blob := []byte(data)
		r.upload(t, g, blob)
		r.commit(t, s, g.Generation, blob, false)
		_ = i
	}
	snap, dlURL, err := r.sm.LatestGrant("u1", "g1")
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if snap.Generation != 2 {
		t.Fatalf("latest should be 2: %+v", snap)
	}
	got, err := r.objs.DownloadURL(context.Background(), dlURL)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if string(got) != "v2-data" || sha(got) != snap.SHA256 {
		t.Fatal("restored bytes do not match committed snapshot")
	}
	hist, err := r.sm.Saves.History("u1", "g1", 10)
	if err != nil || len(hist) != 2 || hist[0].Generation != 2 {
		t.Fatalf("history wrong: %+v %v", hist, err)
	}
}

func TestSaveCorruptAndFailedPaths(t *testing.T) {
	r := newSaveRig(t)
	s := r.enrollSession("n1")
	g, err := r.sm.Begin(s.SessionID, "n1", s.FenceToken)
	if err != nil {
		t.Fatal(err)
	}
	// Tampered upload is caught at PUT time.
	if err := r.objs.UploadURL(context.Background(), g.UploadURL,
		bytes.NewReader([]byte("tampered")), sha([]byte("original"))); err == nil {
		t.Fatal("tampered upload must fail sha check")
	}
	// Commit with malformed sha is rejected and marked CORRUPT.
	_, err = r.sm.Commit(controlSaves.CommitParams{
		SessionID: s.SessionID, NodeID: "n1", Token: s.FenceToken,
		Generation: g.Generation, SHA256: "nope", SizeBytes: 4,
	})
	if err == nil {
		t.Fatal("malformed sha commit must fail")
	}
	stored, _ := r.sm.Saves.Get("u1", "g1", g.Generation)
	if stored.State != protocol.SaveCorrupt {
		t.Fatalf("bad commit should mark CORRUPT: %+v", stored)
	}
	if ptr, _ := r.sm.Saves.Pointer("u1", "g1"); ptr != 0 {
		t.Fatalf("pointer must stay 0, got %d", ptr)
	}
	// Commit of a blob that was never uploaded: structurally accepted
	// (control cannot see R2 bytes), but restore then fails honestly.
	g2, _ := r.sm.Begin(s.SessionID, "n1", s.FenceToken)
	blob := []byte("ghost")
	snap, err := r.sm.Commit(controlSaves.CommitParams{
		SessionID: s.SessionID, NodeID: "n1", Token: s.FenceToken,
		Generation: g2.Generation, SHA256: sha(blob), SizeBytes: uint64(len(blob)),
	})
	if err != nil {
		t.Fatalf("structural commit: %v", err)
	}
	_ = snap
	_, _, err = r.sm.LatestGrant("u1", "g1")
	if err == nil {
		t.Fatal("restore of missing blob must fail (no silent success)")
	}
}

func TestSaveFenceInvalid(t *testing.T) {
	r := newSaveRig(t)
	s := r.enrollSession("n1")
	g, err := r.sm.Begin(s.SessionID, "n1", s.FenceToken)
	if err != nil {
		t.Fatal(err)
	}
	blob := []byte("data")
	r.upload(t, g, blob)
	// Wrong token: commit refused, generation orphaned, pointer untouched.
	_, err = r.sm.Commit(controlSaves.CommitParams{
		SessionID: s.SessionID, NodeID: "n1", Token: 9999,
		Generation: g.Generation, SHA256: sha(blob), SizeBytes: uint64(len(blob)),
	})
	if err == nil {
		t.Fatal("stale-token commit must be refused")
	}
	stored, _ := r.sm.Saves.Get("u1", "g1", g.Generation)
	if stored.State != protocol.SaveOrphaned {
		t.Fatalf("zombie upload must be ORPHANED: %+v", stored)
	}
	if ptr, _ := r.sm.Saves.Pointer("u1", "g1"); ptr != 0 {
		t.Fatalf("pointer must stay 0, got %d", ptr)
	}
	// Zombie cannot even begin.
	if _, err := r.sm.Begin(s.SessionID, "n1", 9999); err == nil {
		t.Fatal("zombie begin must be refused")
	}
}

// TestZombieChaos is the permanent regression test: node A saves gen 41,
// re-enrolls (partition recovery) fencing its old session, node B's agent
// saves gen 42, and A's stale commits can never move latest off 42.
func TestZombieChaos(t *testing.T) {
	r := newSaveRig(t)
	sA := r.enrollSession("n1")
	// 40 prior generations to reach the literal 41/42 narrative.
	for i := 0; i < 40; i++ {
		g, err := r.sm.Begin(sA.SessionID, "n1", sA.FenceToken)
		if err != nil {
			t.Fatal(err)
		}
		blob := []byte{byte(i)}
		r.upload(t, g, blob)
		r.commit(t, sA, g.Generation, blob, true)
	}
	g41, err := r.sm.Begin(sA.SessionID, "n1", sA.FenceToken)
	if err != nil {
		t.Fatal(err)
	}
	blob41 := []byte("gen-41-data")
	r.upload(t, g41, blob41)
	r.commit(t, sA, g41.Generation, blob41, false)
	if ptr, _ := r.sm.Saves.Pointer("u1", "g1"); ptr != 41 {
		t.Fatalf("pointer should be 41, got %d", ptr)
	}

	// Network partition heals via re-enrollment: new token fences sessA.
	n1, _ := r.reg.Enroll("n1", capableCaps())
	r.sess.FenceStaleSessions("n1", n1.FenceToken)
	got, _ := r.sess.Store.Get(sA.SessionID)
	if got.State != protocol.SessionFenced {
		t.Fatalf("re-enroll must fence old session: %+v", got.State)
	}

	// Fresh agent (new token) starts sessB and saves gen 42.
	sB, err := r.sess.Create("u1", "g1")
	if err != nil {
		t.Fatalf("create B: %v", err)
	}
	if sB.NodeID != "n1" || sB.FenceToken != n1.FenceToken {
		t.Fatalf("B should hold the new token: %+v", sB)
	}
	g42, err := r.sm.Begin(sB.SessionID, "n1", sB.FenceToken)
	if err != nil {
		t.Fatalf("B begin: %v", err)
	}
	if g42.Generation != 42 {
		t.Fatalf("expected gen 42, got %d", g42.Generation)
	}
	blob42 := []byte("gen-42-data")
	r.upload(t, g42, blob42)
	r.commit(t, sB, g42.Generation, blob42, false)

	// Zombie A returns with the stale token: every path refused.
	if _, err := r.sm.Begin(sA.SessionID, "n1", sA.FenceToken); err == nil {
		t.Fatal("zombie begin must be refused")
	}
	stale := []byte("zombie-overwrite")
	_, err = r.sm.Commit(controlSaves.CommitParams{
		SessionID: sA.SessionID, NodeID: "n1", Token: sA.FenceToken,
		Generation: 41, SHA256: sha(stale), SizeBytes: uint64(len(stale)),
	})
	if err == nil {
		t.Fatal("zombie commit must be refused")
	}
	latest, _, err := r.sm.LatestGrant("u1", "g1")
	if err != nil {
		t.Fatal(err)
	}
	if latest.Generation != 42 || string(mustDownload(t, r, latest)) != "gen-42-data" {
		t.Fatalf("latest MUST remain 42 with B's bytes: %+v", latest)
	}
}

func mustDownload(t *testing.T, r *saveRig, snap *protocol.SaveSnapshot) []byte {
	t.Helper()
	_, dlURL, err := r.sm.LatestGrant(snap.UserID, snap.GameID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.objs.DownloadURL(context.Background(), dlURL)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCrashFinalSaveAndRestart(t *testing.T) {
	r := newSaveRig(t)
	s := r.enrollSession("n1")
	g, _ := r.sm.Begin(s.SessionID, "n1", s.FenceToken)
	blob := []byte("checkpoint")
	r.upload(t, g, blob)
	r.commit(t, s, g.Generation, blob, true) // mid-session CHECKPOINT
	// Game crashes: final save attempt then close with crash reason.
	g2, _ := r.sm.Begin(s.SessionID, "n1", s.FenceToken)
	blob2 := []byte("final")
	r.upload(t, g2, blob2)
	r.commit(t, s, g2.Generation, blob2, false)
	closed, err := r.sess.Close(s.SessionID, "n1", s.FenceToken, protocol.EndGameCrash)
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if closed.EndReason != protocol.EndGameCrash || len(closed.SaveGens) != 2 {
		t.Fatalf("crash close wrong: %+v", closed)
	}
	// Control-plane restart: new managers over the SAME stores continue
	// generations; the pointer survives.
	sess2 := csession.NewManager(r.sess.Store, r.cat, r.reg)
	sm2 := controlSaves.NewManager(r.sm.Saves, r.objs, sess2)
	s2 := r.enrollSession("n1")
	g3, err := sm2.Begin(s2.SessionID, "n1", s2.FenceToken)
	if err != nil {
		t.Fatalf("post-restart begin: %v", err)
	}
	if g3.Generation != 3 {
		t.Fatalf("generations must continue after restart, got %d", g3.Generation)
	}
}

func TestSaveAPIEndToEnd(t *testing.T) {
	r := newSaveRig(t)
	r.reg.Enroll("n1", capableCaps())
	srv := api.NewWithSessions(r.cat, r.reg, r.sess)
	// Swap in our rig's save manager (memory objects) for determinism.
	srv.Saves = r.sm
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
	code, body := post("/v1/sessions", map[string]any{"user_id": "u1", "game_id": "g1"})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %+v", code, body)
	}
	id := body["session_id"].(string)
	token := body["fence_token"].(float64)

	code, grant := post("/v1/sessions/"+id+"/saves/begin",
		map[string]any{"node_id": "n1", "fence_token": token})
	if code != http.StatusCreated || grant["generation"] == nil || grant["upload_url"] == nil {
		t.Fatalf("begin: %d %+v", code, grant)
	}
	gen := uint64(grant["generation"].(float64))
	blob := []byte("api-save")
	if err := r.objs.UploadURL(context.Background(), grant["upload_url"].(string),
		bytes.NewReader(blob), sha(blob)); err != nil {
		t.Fatal(err)
	}
	code, snap := post("/v1/sessions/"+id+"/saves/commit", map[string]any{
		"node_id": "n1", "fence_token": token, "generation": gen,
		"sha256": sha(blob), "size_bytes": len(blob), "file_count": 1,
	})
	if code != http.StatusOK || snap["state"] != "VALID" {
		t.Fatalf("commit: %d %+v", code, snap)
	}
	resp, err := http.Get(ts.URL + "/v1/sessions/" + id + "/saves/latest")
	if err != nil {
		t.Fatal(err)
	}
	var latest map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&latest)
	resp.Body.Close()
	if latest["snapshot"] == nil || latest["download_url"] == nil {
		t.Fatalf("latest: %+v", latest)
	}
	// Zombie token over HTTP: 409, pointer untouched.
	code, _ = post("/v1/sessions/"+id+"/saves/commit", map[string]any{
		"node_id": "n1", "fence_token": 4242, "generation": gen,
		"sha256": sha(blob), "size_bytes": len(blob),
	})
	if code != http.StatusConflict {
		t.Fatalf("zombie HTTP commit should 409, got %d", code)
	}
}
