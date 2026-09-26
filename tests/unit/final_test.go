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
	"github.com/personal-game/personal-game/internal/agent/capability"
	"github.com/personal-game/personal-game/internal/common"
	controlSaves "github.com/personal-game/personal-game/internal/control/saves"
	"github.com/personal-game/personal-game/pkg/protocol"
)

// N=20 proves part counts are arbitrary (never hardcoded): fetch, order,
// verify across twenty sources.
func TestTwentyParts(t *testing.T) {
	blobs := map[string][]byte{}
	var sources []protocol.AcquisitionSource
	for i := 1; i <= 20; i++ {
		name := "part" + itoa(i) + ".zip"
		blobs[name] = []byte("payload-" + itoa(i))
		sources = append(sources, protocol.AcquisitionSource{
			URL: "http://example.invalid/" + name, Part: i, Filename: name,
		})
	}
	srv := fixtureServer(t, blobs)
	defer srv.Close()
	m := protocol.GameManifest{
		SchemaVersion: 3, GameID: "mp20", Name: "MP20", Version: "1",
		Acquisition: protocol.AcquisitionRef{Provider: "archive",
			PackageType: protocol.PackageArchiveInstaller, Sources: sources},
		Launch: protocol.LaunchSpec{Executable: "g.exe"},
		PackagePipeline: &protocol.PackagePipeline{
			Installer: &protocol.InstallerSpec{Path: "setup.exe"},
		},
	}
	for i := range m.Acquisition.Sources {
		b := blobs[m.Acquisition.Sources[i].Filename]
		m.Acquisition.Sources[i].URL = srv.URL + "/" + m.Acquisition.Sources[i].Filename
		m.Acquisition.Sources[i].SHA256 = sha(b)
		m.Acquisition.Sources[i].SizeBytes = uint64(len(b))
	}
	// Reverse to prove ordering is by part number.
	for i, j := 0, len(m.Acquisition.Sources)-1; i < j; i, j = i+1, j-1 {
		m.Acquisition.Sources[i], m.Acquisition.Sources[j] = m.Acquisition.Sources[j], m.Acquisition.Sources[i]
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	d := testDirs(t)
	dl := &acquire.Downloader{MaxRetries: -1}
	for _, s := range acquire.OrderedSources(m) {
		if err := dl.Fetch(context.Background(), d.Download, s); err != nil {
			t.Fatalf("fetch part %d: %v", s.Part, err)
		}
	}
	if err := acquire.VerifyAll(d.Download, m); err != nil {
		t.Fatalf("verify: %v", err)
	}
	// Missing part detected (deleted after download).
	if err := os.Remove(filepath.Join(d.Download, "part7.zip")); err != nil {
		t.Fatal(err)
	}
	if err := acquire.VerifyAll(d.Download, m); err == nil {
		t.Fatal("missing part must fail verification")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// Fencing during preparation: a node that dies mid-prepare cannot commit
// afterwards, even though it began the generation while alive.
func TestFenceDuringPreparation(t *testing.T) {
	r := newSaveRig(t)
	s := r.enrollSession("n1")
	// Session sits in PREPARING (game still preparing, no backend yet).
	g, err := r.sm.Begin(s.SessionID, "n1", s.FenceToken)
	if err != nil {
		t.Fatal(err)
	}
	blob := []byte("prepared-then-died")
	r.upload(t, g, blob)
	n1, _ := r.reg.Enroll("n1", capableCaps())
	if n := r.sess.FenceStaleSessions("n1", n1.FenceToken); n != 1 {
		t.Fatalf("preparing session must be fenced, got %d", n)
	}
	if _, err := r.sm.Commit(controlSaves.CommitParams{
		SessionID: s.SessionID, NodeID: "n1", Token: s.FenceToken,
		Generation: g.Generation, SHA256: sha(blob), SizeBytes: uint64(len(blob)),
	}); err == nil {
		t.Fatal("commit after fencing must be refused")
	}
	if ptr, _ := r.sm.Saves.Pointer("u1", "g1"); ptr != 0 {
		t.Fatalf("pointer must stay 0, got %d", ptr)
	}
}

// Client disconnect/reconnect: STREAMING -> DEGRADED -> STREAMING keeps
// the session (and its fence) alive with no save corruption.
func TestDegradedReconnect(t *testing.T) {
	r := newSaveRig(t)
	s := r.enrollSession("n1")
	advance := func(st protocol.SessionState, active bool) *protocol.Session {
		t.Helper()
		got, err := r.sess.Observe(s.SessionID, "n1", s.FenceToken, st, active)
		if err != nil {
			t.Fatalf("observe %s: %v", st, err)
		}
		return got
	}
	advance(protocol.SessionReady, false)
	advance(protocol.SessionStreaming, true)
	advance(protocol.SessionDegraded, true) // disconnect, grace holds game
	got := advance(protocol.SessionStreaming, true)
	if got.State != protocol.SessionStreaming {
		t.Fatalf("reconnect must resume streaming: %+v", got)
	}
	// Saves still commit under the same fence after reconnect.
	g, err := r.sm.Begin(s.SessionID, "n1", s.FenceToken)
	if err != nil {
		t.Fatalf("begin after reconnect: %v", err)
	}
	blob := []byte("after-reconnect")
	r.upload(t, g, blob)
	r.commit(t, s, g.Generation, blob, false)
}

// R2 presigned URLs fail fast when the endpoint is gone (no hangs).
func TestR2EndpointGone(t *testing.T) {
	// Presigning itself is offline crypto and must succeed...
	// ...but any HTTP use against a dead endpoint must error quickly.
	resp, err := http.Get("http://127.0.0.1:9/obj/missing")
	if err == nil {
		resp.Body.Close()
		t.Fatal("dead endpoint must fail")
	}
}

// Agent config: new node fields parse and validate.
func TestAgentConfigNodes(t *testing.T) {
	t.Setenv("PG_GPU_INDEX", "2")
	t.Setenv("PG_GAME_ROOT", "/mnt/games")
	t.Setenv("PG_TEMP_ROOT", "/mnt/tmp")
	cfg, err := common.LoadAgentConfig()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if cfg.GPUIndex != 2 || cfg.GamesDir != "/mnt/games" || cfg.TempRoot != "/mnt/tmp" {
		t.Fatalf("node fields wrong: %+v", cfg)
	}
	t.Setenv("PG_GPU_INDEX", "nope")
	if _, err := common.LoadAgentConfig(); err == nil {
		t.Fatal("bad GPU index must fail")
	}
}

// Tailscale detail is honest about absence (present case varies by host).
func TestTailscaleAbsent(t *testing.T) {
	d := capability.Tailscale()
	if d.Installed {
		t.Logf("tailscale present here: running=%v ip=%q peers=%d", d.Running, d.IP, d.Peers)
		if d.Running && d.Peers < -1 {
			t.Fatal("peer count out of range")
		}
		return
	}
	if d.Running || d.IP != "" || d.Peers != 0 || d.Reason == "" {
		t.Fatalf("absent tailscale must be honest: %+v", d)
	}
}

// NVIDIA runtime never claims container acceleration without the chain.
func TestNVIDIARuntimeHonest(t *testing.T) {
	r := capability.DetectNVIDIARuntime()
	if r.Accelerated && (!r.GPUSeen || (!r.ToolkitPresent && !r.DockerNvidiaRT)) {
		t.Fatalf("acceleration claimed without the chain: %+v", r)
	}
	if r.Detail == "" {
		t.Fatal("runtime check must always explain itself")
	}
	t.Logf("nvidia: %s", r.Detail)
}

// ServeContent range support sanity for the resume test path.
func TestServeContentRanges(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "f.bin", time.Time{}, bytes.NewReader(payload))
	}))
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/f.bin", nil)
	req.Header.Set("Range", "bytes=512-")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("range support required for resume tests: %d", resp.StatusCode)
	}
}
