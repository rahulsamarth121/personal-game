package unit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/personal-game/personal-game/internal/agent/acquire"
	"github.com/personal-game/personal-game/internal/agent/cache"
	"github.com/personal-game/personal-game/internal/agent/capability"
	agentSaves "github.com/personal-game/personal-game/internal/agent/saves"
	"github.com/personal-game/personal-game/internal/control/api"
	"github.com/personal-game/personal-game/internal/control/nodes"
	csession "github.com/personal-game/personal-game/internal/control/session"
	"github.com/personal-game/personal-game/pkg/protocol"
)

func steamManifest(ref map[string]any) protocol.GameManifest {
	return protocol.GameManifest{
		SchemaVersion: 3, GameID: "sg", Name: "SG", Version: "1",
		Acquisition: protocol.AcquisitionRef{Provider: "steamcmd", Reference: ref,
			Sources: []protocol.AcquisitionSource{{URL: "steam://730", Part: 1, Filename: "x"}}},
		Launch: protocol.LaunchSpec{Executable: "g.exe"},
	}
}

func TestSteamCMDConfig(t *testing.T) {
	games := t.TempDir()
	p, err := acquire.SteamCMDConfig(steamManifest(map[string]any{"app_id": float64(730)}), games)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if p.AppID != "730" || p.Username != "anonymous" || p.Timeout != 2*time.Hour {
		t.Fatalf("defaults wrong: %+v", p)
	}
	args := p.Args()
	want := []string{"+login", "anonymous", "+force_install_dir", p.InstallDir, "+app_update", "730", "+quit"}
	if len(args) != len(want) {
		t.Fatalf("argv wrong: %+v", args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("argv[%d]=%q want %q", i, args[i], want[i])
		}
	}
	if _, err := acquire.SteamCMDConfig(steamManifest(nil), games); err == nil {
		t.Fatal("missing app_id must fail")
	}
	if _, err := acquire.SteamCMDConfig(steamManifest(map[string]any{"app_id": "440", "validate": true}), games); err != nil {
		t.Fatalf("string app_id must work: %v", err)
	}
	// Unknown providers still refuse loudly via the manifest resolver.
	if _, err := acquire.ProviderForManifest(steamManifest(map[string]any{"app_id": "1"}), nil, games); err != nil {
		t.Fatalf("steamcmd must resolve: %v", err)
	}
	m := steamManifest(map[string]any{"app_id": "1"})
	m.Acquisition.Provider = "epic"
	if _, err := acquire.ProviderForManifest(m, nil, games); err == nil {
		t.Fatal("unknown provider must refuse")
	}
}

func TestSteamCMDMissingBinary(t *testing.T) {
	if _, err := os.Stat(`C:\nonexistent-steamcmd-binary`); !os.IsNotExist(err) {
		t.Skip("unexpected")
	}
	games := t.TempDir()
	p, err := acquire.SteamCMDConfig(steamManifest(map[string]any{"app_id": "1"}), games)
	if err != nil {
		t.Fatal(err)
	}
	// PATH manipulation is process-global; instead assert the idempotent
	// fast path (non-empty dir skips execution entirely).
	if err := os.MkdirAll(p.InstallDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.InstallDir, "game.exe"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := p.Fetch(context.Background(), t.TempDir(),
		protocol.AcquisitionSource{Part: 1}); err != nil {
		t.Fatalf("populated dir must skip execution: %v", err)
	}
	// Empty dir with no binary: structured failure, never silent success.
	p2, _ := acquire.SteamCMDConfig(steamManifest(map[string]any{"app_id": "1"}), t.TempDir())
	if err := p2.Fetch(context.Background(), t.TempDir(),
		protocol.AcquisitionSource{Part: 1}); err == nil {
		t.Fatal("missing steamcmd binary must fail")
	} else if got := err.Error(); len(got) < 10 || !containsStr(got, "steamcmd") {
		t.Fatalf("error must name the dependency: %q", got)
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestCacheRegistry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	r, err := cache.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.MarkInstalled("a", "1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.MarkInstalled("b", "1", 200); err != nil {
		t.Fatal(err)
	}
	// Persistence across reopen.
	r2, err := cache.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := r2.Evictable("zzz"); got != 300 {
		t.Fatalf("evictable should be 300, got %d", got)
	}
	// Active game excluded; protected excluded.
	if got := r2.Evictable("a"); got != 200 {
		t.Fatalf("active excluded: %d", got)
	}
	if err := r2.Protect("b", true); err != nil {
		t.Fatal(err)
	}
	if got := r2.Evictable("zzz"); got != 100 {
		t.Fatalf("protected excluded: %d", got)
	}
	if err := r2.Protect("b", false); err != nil {
		t.Fatal(err)
	}
	victims, ok := r2.Victims("zzz", 150)
	if !ok || len(victims) == 0 {
		t.Fatalf("victims should cover 150: %+v %v", victims, ok)
	}
	if err := r2.Remove("a"); err != nil {
		t.Fatal(err)
	}
	if got := r2.Evictable("zzz"); got != 200 {
		t.Fatalf("after remove: %d", got)
	}
	// Corrupt index fails loudly (files stay authoritative).
	if err := os.WriteFile(path, []byte("{bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Open(path); err == nil {
		t.Fatal("corrupt registry must fail")
	}
	// Missing file starts empty.
	r3, err := cache.Open(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := r3.Evictable("x"); got != 0 {
		t.Fatalf("empty registry: %d", got)
	}
}

func TestTailscaleDetail(t *testing.T) {
	d := capability.Tailscale()
	if d.Installed {
		t.Logf("tailscale present: running=%v ip=%q reason=%q", d.Running, d.IP, d.Reason)
		if d.Running && d.IP == "" && d.Reason == "" {
			t.Fatal("running without IP needs a reason")
		}
		return
	}
	if d.Running || d.IP != "" || d.Reason == "" {
		t.Fatalf("absent tailscale must be honest: %+v", d)
	}
}

func TestLudusaviMissing(t *testing.T) {
	if agentSaves.HasLudusavi() {
		t.Skip("ludusavi installed: negative path not applicable")
	}
	err := agentSaves.BackupTo(context.Background(), "Some Game", t.TempDir())
	if err == nil {
		t.Fatal("backup without binary must fail")
	}
	err = agentSaves.RestoreFrom(context.Background(), "Some Game", t.TempDir())
	if err == nil {
		t.Fatal("restore without binary must fail")
	}
}

func TestClientShellLibraryHistory(t *testing.T) {
	cat := testCatalog(t)
	reg := nodes.New()
	mgr := csession.NewManager(csession.NewMemoryStore(), cat, reg)
	srv := api.NewWithSessions(cat, reg, mgr)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/v1/games")
	if err != nil {
		t.Fatal(err)
	}
	var lib struct {
		Games []protocol.GameManifest `json:"games"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&lib); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(lib.Games) != 1 || lib.Games[0].GameID != "doom2" {
		t.Fatalf("library wrong: %+v", lib)
	}
	resp, err = http.Get(ts.URL + "/v1/stats/playtime?user_id=u1")
	if err != nil {
		t.Fatal(err)
	}
	var pt struct {
		Playtime []map[string]any `json:"playtime"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pt); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if pt.Playtime == nil {
		t.Fatal("history must return a list (possibly empty), never null-shape")
	}
}
