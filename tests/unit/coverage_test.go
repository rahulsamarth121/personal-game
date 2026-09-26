package unit

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/personal-game/personal-game/internal/agent/acquire"
	"github.com/personal-game/personal-game/internal/agent/backend"
	"github.com/personal-game/personal-game/internal/agent/cache"
	agentSaves "github.com/personal-game/personal-game/internal/agent/saves"
	controlSaves "github.com/personal-game/personal-game/internal/control/saves"
	"github.com/personal-game/personal-game/pkg/protocol"
)

func TestManyPartsFetchVerify(t *testing.T) {
	blobs := map[string][]byte{}
	var sources []protocol.AcquisitionSource
	for i := 1; i <= 5; i++ {
		name := string([]byte{'g', '.', 'p', 'a', 'r', 't', '0', byte('0' + i), '.', 'z', 'i', 'p'})
		blobs[name] = []byte("bytes-part-" + string([]byte{byte('0' + i)}))
		sources = append(sources, protocol.AcquisitionSource{
			URL: "http://example.invalid/" + name, Part: i, Filename: name,
		})
	}
	srv := fixtureServer(t, blobs)
	defer srv.Close()
	m := protocol.GameManifest{
		SchemaVersion: 3, GameID: "mp", Name: "MP", Version: "1",
		Acquisition: protocol.AcquisitionRef{Provider: "archive",
			PackageType: protocol.PackageArchiveInstaller, Sources: sources},
		Launch: protocol.LaunchSpec{Executable: "g.exe"},
		PackagePipeline: &protocol.PackagePipeline{
			Installer: &protocol.InstallerSpec{Path: "setup.exe"},
		},
	}
	// Repoint at the fixture server with real checksums, deliberately
	// shuffled to prove ordering comes from part numbers, not input order.
	for i := range m.Acquisition.Sources {
		b := blobs[m.Acquisition.Sources[i].Filename]
		m.Acquisition.Sources[i].URL = srv.URL + "/" + m.Acquisition.Sources[i].Filename
		m.Acquisition.Sources[i].SHA256 = sha(b)
		m.Acquisition.Sources[i].SizeBytes = uint64(len(b))
	}
	m.Acquisition.Sources[0], m.Acquisition.Sources[4] = m.Acquisition.Sources[4], m.Acquisition.Sources[0]
	if err := m.Validate(); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	ordered := acquire.OrderedSources(m)
	for i, s := range ordered {
		if s.Part != i+1 {
			t.Fatalf("order wrong at %d: %+v", i, s)
		}
	}
	d := testDirs(t)
	dl := &acquire.Downloader{MaxRetries: -1}
	for _, s := range ordered {
		if err := dl.Fetch(context.Background(), d.Download, s); err != nil {
			t.Fatalf("fetch part %d: %v", s.Part, err)
		}
	}
	if err := acquire.VerifyAll(d.Download, m); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestInterruptedDownloadResumes(t *testing.T) {
	payload := bytes.Repeat([]byte("0123456789abcdef"), 64*1024) // 1 MiB
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "big.bin", time.Time{}, bytes.NewReader(payload))
	}))
	defer srv.Close()
	// Simulate an interrupted first attempt: half the bytes on disk.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "big.bin"), payload[:len(payload)/2], 0o644); err != nil {
		t.Fatal(err)
	}
	s := protocol.AcquisitionSource{URL: srv.URL + "/big.bin", Part: 1,
		Filename: "big.bin", SHA256: sha(payload), SizeBytes: uint64(len(payload))}
	dl := &acquire.Downloader{MaxRetries: -1}
	if err := dl.Fetch(context.Background(), dir, s); err != nil {
		t.Fatalf("resume: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "big.bin"))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatal("resumed bytes differ")
	}
}

func TestCorruptArchiveFails(t *testing.T) {
	dest := t.TempDir()
	bad := filepath.Join(t.TempDir(), "bad.zip")
	if err := os.WriteFile(bad, []byte("definitely not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := acquire.ExtractZip(bad, dest); err == nil {
		t.Fatal("corrupt archive must fail extraction")
	}
}

func TestInstallerFailureSurfaces(t *testing.T) {
	// Hermetic failing installer: this test binary with a bogus flag exits
	// non-zero fast on every platform.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	selfBytes, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	stage := t.TempDir()
	if err := os.WriteFile(filepath.Join(stage, "setup.exe"), selfBytes, 0o755); err != nil {
		t.Fatal(err)
	}
	games := t.TempDir()
	spec := protocol.InstallerSpec{Path: "setup.exe", Arguments: []string{"-test.unknownflag"}, TimeoutSecs: 60}
	err = acquire.RunInstaller(context.Background(), stage, spec, games, "g")
	if err == nil {
		t.Fatal("failing installer must surface its exit code")
	}
}

func TestISONeedsSevenZip(t *testing.T) {
	if acquire.Has7z() {
		t.Skip("7z present: extraction path covered elsewhere")
	}
	err := acquire.ExtractISO(context.Background(),
		filepath.Join(t.TempDir(), "game.iso"), t.TempDir())
	if err == nil {
		t.Fatal("ISO without 7z must fail clearly")
	}
}

func TestNodeDeathDuringSave(t *testing.T) {
	r := newSaveRig(t)
	s := r.enrollSession("n1")
	g, err := r.sm.Begin(s.SessionID, "n1", s.FenceToken)
	if err != nil {
		t.Fatal(err)
	}
	blob := []byte("unsaved-progress")
	r.upload(t, g, blob) // bytes uploaded, commit never arrives: node dies
	// Control plane notices the dead node when the replacement enrolls;
	// the old session is fenced and the pending commit is refused.
	n1, _ := r.reg.Enroll("n1", capableCaps())
	r.sess.FenceStaleSessions("n1", n1.FenceToken)
	_, err = r.sm.Commit(controlSaves.CommitParams{
		SessionID: s.SessionID, NodeID: "n1", Token: s.FenceToken,
		Generation: g.Generation, SHA256: sha(blob), SizeBytes: uint64(len(blob)),
	})
	if err == nil {
		t.Fatal("commit after node death must be refused")
	}
	stored, _ := r.sm.Saves.Get("u1", "g1", g.Generation)
	if stored.State != protocol.SaveOrphaned {
		t.Fatalf("dead-node upload must be ORPHANED: %+v", stored)
	}
	if ptr, _ := r.sm.Saves.Pointer("u1", "g1"); ptr != 0 {
		t.Fatalf("pointer must stay 0, got %d", ptr)
	}
}

func TestUploadPUTFailure(t *testing.T) {
	f := filepath.Join(t.TempDir(), "s.zip")
	if err := os.WriteFile(f, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Nothing listens here: connection refused, fast.
	if err := agentSaves.UploadPUT("http://127.0.0.1:9/upload", f, sha([]byte("data"))); err == nil {
		t.Fatal("unreachable storage must fail the upload")
	}
}

func TestEvictForNeed(t *testing.T) {
	base := t.TempDir()
	games := filepath.Join(base, "games")
	mkGame := func(id string, size int) {
		dir := filepath.Join(games, id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "game.exe"), bytes.Repeat([]byte("x"), size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mkGame("old", 1000)
	mkGame("new", 2000)
	mkGame("active", 5000)
	savesSentinel := filepath.Join(base, "saves", "keep.sav")
	if err := os.MkdirAll(filepath.Join(base, "saves"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(savesSentinel, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, err := cache.Open(filepath.Join(base, "state", "cache.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"old", "new", "active"} {
		if err := reg.MarkInstalled(id, "1", 1000); err != nil {
			t.Fatal(err)
		}
	}
	// Force deterministic LRU order via protected/active flags is timing
	// dependent; instead assert the safety invariants on a small need.
	freed, ok := cache.EvictForNeed(games, reg, "active", 500)
	if !ok || freed < 500 {
		t.Fatalf("eviction should cover 500: freed=%d ok=%v", freed, ok)
	}
	if _, err := os.Stat(filepath.Join(games, "active", "game.exe")); err != nil {
		t.Fatal("active game must survive eviction")
	}
	if _, err := os.Stat(savesSentinel); err != nil {
		t.Fatal("saves must never be touched by cache eviction")
	}
	// Impossible need: nothing deleted.
	freed, ok = cache.EvictForNeed(games, reg, "active", 1<<40)
	if ok || freed != 0 {
		t.Fatalf("uncoverable need must fail without deleting: %d %v", freed, ok)
	}
}

func TestFakeConnectionNeedsHost(t *testing.T) {
	fb := backend.NewFakeBackend()
	game := backend.Game{}
	game.Manifest.GameID = "g"
	if _, err := fb.Connection(game); err == nil {
		t.Fatal("connection without host must fail")
	}
}

func TestLudusaviTitleFallsBack(t *testing.T) {
	if agentSaves.HasLudusavi() {
		t.Skip("ludusavi installed: fallback path not applicable")
	}
	mode, reason := agentSaves.ModeFor("Some Game", true)
	if mode != agentSaves.SaveModeOverrides || reason == "" {
		t.Fatalf("title without binary but with overrides must fall back: %v %q", mode, reason)
	}
}
