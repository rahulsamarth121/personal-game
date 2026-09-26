package unit

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/personal-game/personal-game/internal/agent/acquire"
	agentSaves "github.com/personal-game/personal-game/internal/agent/saves"
	"github.com/personal-game/personal-game/internal/control/api"
	"github.com/personal-game/personal-game/internal/control/catalog"
	"github.com/personal-game/personal-game/internal/control/nodes"
	csession "github.com/personal-game/personal-game/internal/control/session"
	"github.com/personal-game/personal-game/pkg/protocol"
)

func writeManifestFile(t *testing.T, dir, name, gameID string) {
	t.Helper()
	m := protocol.GameManifest{
		SchemaVersion: 1, GameID: gameID, Name: gameID, Version: "1",
		Acquisition: protocol.AcquisitionRef{Provider: "archive"},
		Launch:      protocol.LaunchSpec{Executable: "g.exe"},
	}
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogSeedDir(t *testing.T) {
	dir := t.TempDir()
	writeManifestFile(t, dir, "a.json", "game-a")
	writeManifestFile(t, dir, "skip.example.json", "game-skip")
	c := catalog.New()
	ids, err := c.LoadDir(dir)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if len(ids) != 1 || ids[0] != "game-a" {
		t.Fatalf("expected [game-a], got %+v", ids)
	}
	if _, err := c.Get("game-skip"); err == nil {
		t.Fatal(".example.json must not be seeded")
	}
	// Invalid file aborts loudly.
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.LoadDir(dir); err == nil {
		t.Fatal("invalid manifest must abort seeding")
	}
	// Missing dir is a clear error, not an empty catalog.
	if _, err := catalog.New().LoadDir(filepath.Join(dir, "absent")); err == nil {
		t.Fatal("missing seed dir must fail")
	}
}

func TestObserveAndStreamRoutes(t *testing.T) {
	cat := testCatalog(t)
	reg := nodes.New()
	reg.Enroll("n1", capableCaps())
	mgr := csession.NewManager(csession.NewMemoryStore(), cat, reg)
	srv := api.NewWithSessions(cat, reg, mgr)
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
	get := func(path string) (int, map[string]any) {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	code, body := post("/v1/sessions", map[string]any{"user_id": "u1", "game_id": "doom2"})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %+v", code, body)
	}
	id := body["session_id"].(string)
	token := body["fence_token"].(float64)

	// Real observation path: PREPARING -> READY -> STREAMING.
	for _, st := range []string{"READY", "STREAMING"} {
		code, body = post("/v1/sessions/"+id+"/observe",
			map[string]any{"node_id": "n1", "fence_token": token, "state": st, "game_active": true})
		if code != 200 || body["state"] != st {
			t.Fatalf("observe %s: %d %+v", st, code, body)
		}
	}
	// Illegal jump is refused, not applied.
	code, _ = post("/v1/sessions/"+id+"/observe",
		map[string]any{"node_id": "n1", "fence_token": token, "state": "REQUESTED"})
	if code == 200 {
		t.Fatal("illegal transition must be refused")
	}
	// Zombie observation: 409.
	code, _ = post("/v1/sessions/"+id+"/observe",
		map[string]any{"node_id": "n1", "fence_token": 9999, "state": "DEGRADED"})
	if code != http.StatusConflict {
		t.Fatalf("zombie observe should 409, got %d", code)
	}
	// Stream info published by the live holder only.
	code, _ = post("/v1/sessions/"+id+"/stream",
		map[string]any{"node_id": "n1", "fence_token": token,
			"provider": "wolf", "host": "100.64.0.2", "app": "Doom"})
	if code != 200 {
		t.Fatalf("set stream: %d", code)
	}
	code, body = get("/v1/sessions/" + id)
	if code != 200 || body["stream"].(map[string]any)["host"] != "100.64.0.2" {
		t.Fatalf("stream not stored: %d %+v", code, body)
	}
	// Manifest routes for node preparation.
	if code, _ := get("/v1/games/doom2/manifest"); code != 200 {
		t.Fatalf("manifest route: %d", code)
	}
	if code, _ := get("/v1/games/nope/manifest"); code != http.StatusNotFound {
		t.Fatalf("unknown game should 404, got %d", code)
	}
}

func TestFileURLFetch(t *testing.T) {
	src := filepath.Join(t.TempDir(), "local.bin")
	if err := os.WriteFile(src, []byte("local-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	url := "file:///" + strings.TrimPrefix(filepath.ToSlash(src), "/")
	s := protocol.AcquisitionSource{URL: url, Part: 1, Filename: "local.bin", SizeBytes: 11}
	dl := &acquire.Downloader{}
	dir := t.TempDir()
	if err := dl.Fetch(context.Background(), dir, s); err != nil {
		t.Fatalf("file fetch: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "local.bin"))
	if err != nil || string(got) != "local-bytes" {
		t.Fatalf("copied bytes wrong: %v %q", err, got)
	}
	// Size mismatch and missing files fail clearly (no retries in tests).
	dl.MaxRetries = -1
	s.SizeBytes = 999
	if err := dl.Fetch(context.Background(), dir, s); err == nil {
		t.Fatal("size mismatch must fail")
	}
	s.URL = "file:///definitely/not/here.bin"
	s.SizeBytes = 0
	if err := dl.Fetch(context.Background(), dir, s); err == nil {
		t.Fatal("missing local file must fail")
	}
}

func TestProviderResolution(t *testing.T) {
	if _, err := acquire.ProviderFor("archive", nil); err != nil {
		t.Fatalf("archive must resolve: %v", err)
	}
	if _, err := acquire.ProviderFor("", nil); err != nil {
		t.Fatalf("empty provider defaults to archive: %v", err)
	}
	if _, err := acquire.ProviderFor("steamcmd", nil); err == nil {
		t.Fatal("steamcmd must refuse clearly until implemented")
	}
}

func TestLudusaviModeSelection(t *testing.T) {
	has := agentSaves.HasLudusavi()
	mode, reason := agentSaves.ModeFor("Some Game", false)
	if has {
		if mode != agentSaves.SaveModeLudusavi || reason == "" {
			t.Fatalf("with binary+title expect ludusavi: %v %q", mode, reason)
		}
	} else if mode != agentSaves.SaveModeNone {
		t.Fatalf("title without binary or overrides must be none: %v", mode)
	}
	mode, _ = agentSaves.ModeFor("", true)
	if mode != agentSaves.SaveModeOverrides {
		t.Fatalf("overrides must select overrides mode: %v", mode)
	}
	mode, reason = agentSaves.ModeFor("", false)
	if mode != agentSaves.SaveModeNone || reason == "" {
		t.Fatalf("nothing configured must be none with reason: %v %q", mode, reason)
	}
}

func TestGamesPostEndpoint(t *testing.T) {
	cat := testCatalog(t)
	reg := nodes.New()
	mgr := csession.NewManager(csession.NewMemoryStore(), cat, reg)
	srv2 := api.NewWithSessions(cat, reg, mgr)
	ts := httptest.NewServer(srv2)
	defer ts.Close()

	// Valid manifest: 201, then visible in the catalog.
	m := protocol.GameManifest{
		SchemaVersion: 3, GameID: "posted", Name: "Posted", Version: "1",
		Acquisition: protocol.AcquisitionRef{Provider: "archive",
			PackageType: protocol.PackageDirectPrebuilt,
			Sources:     []protocol.AcquisitionSource{{URL: "https://example.invalid/g.exe", Part: 1, Filename: "g.exe"}}},
		Launch: protocol.LaunchSpec{Executable: "g.exe"},
	}
	raw, _ := json.Marshal(m)
	resp, err := http.Post(ts.URL+"/v1/games", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST valid manifest: %d", resp.StatusCode)
	}
	if _, err := cat.Get("posted"); err != nil {
		t.Fatalf("posted game not in catalog: %v", err)
	}
	// Invalid manifest (prebuilt + installer): 400 with the reason.
	m.PackagePipeline = &protocol.PackagePipeline{Installer: &protocol.InstallerSpec{Path: "s.exe"}}
	raw, _ = json.Marshal(m)
	resp2, err := http.Post(ts.URL+"/v1/games", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST invalid manifest should 400, got %d", resp2.StatusCode)
	}
}
