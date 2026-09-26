package unit

// Durable GUI onboarding: POST /v1/games must persist the validated manifest
// into the seed directory (PG_MANIFEST_DIR) so the game survives a control
// plane restart, and must report honestly when persistence is impossible.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/personal-game/personal-game/internal/control/api"
	"github.com/personal-game/personal-game/internal/control/catalog"
	"github.com/personal-game/personal-game/internal/control/nodes"
	"github.com/personal-game/personal-game/pkg/protocol"
)

func postGame(t *testing.T, srv *api.Server, m protocol.GameManifest) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/games", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v (%s)", err, rec.Body.String())
	}
	return rec.Code, out
}

func validV3Manifest(gameID string) protocol.GameManifest {
	return protocol.GameManifest{
		SchemaVersion: protocol.ManifestSchemaVersion,
		GameID:        gameID,
		Name:          "Persist Test Game",
		Version:       "1.0",
		Acquisition: protocol.AcquisitionRef{
			Provider:    "archive",
			PackageType: protocol.PackageDirectPrebuilt,
			Sources: []protocol.AcquisitionSource{{
				URL: "https://example.invalid/game.exe", Part: 1, Filename: "game.exe",
			}},
		},
		Runtime: protocol.RuntimeSpec{OS: "windows"},
		Launch:  protocol.LaunchSpec{Executable: "game.exe"},
		Saves:   protocol.SaveSpec{Provider: "ludusavi"},
	}
}

func TestGamesPostPersistsManifestToSeedDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PG_MANIFEST_DIR", dir)

	cat := catalog.New()
	if err := cat.Put(validV3Manifest("seeded-game")); err != nil {
		t.Fatal(err)
	}
	srv := api.New(cat, nodes.New())

	code, out := postGame(t, srv, validV3Manifest("gui-added-game"))
	if code != http.StatusCreated {
		t.Fatalf("status = %d, body %v", code, out)
	}
	if persisted, _ := out["persisted"].(bool); !persisted {
		t.Fatalf("persisted flag not true: %v", out)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "gui-added-game.json"))
	if err != nil {
		t.Fatalf("manifest not persisted: %v", err)
	}
	var back protocol.GameManifest
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.GameID != "gui-added-game" || back.Acquisition.PackageType != protocol.PackageDirectPrebuilt {
		t.Fatalf("persisted manifest wrong: %+v", back)
	}

	// A fresh registry seeded from the same directory must contain the game
	// (i.e. restart would not lose it).
	fresh := catalog.New()
	ids, err := fresh.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, id := range ids {
		if id == "gui-added-game" {
			found = true
		}
	}
	if !found {
		t.Fatalf("re-seeded catalog lost the game; ids=%v", ids)
	}
}

func TestGamesPostReportsUnpersistable(t *testing.T) {
	// A file where the seed directory should be makes MkdirAll fail.
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PG_MANIFEST_DIR", filepath.Join(blocked, "manifests"))

	cat := catalog.New()
	if err := cat.Put(validV3Manifest("seeded-game")); err != nil {
		t.Fatal(err)
	}
	srv := api.New(cat, nodes.New())

	code, out := postGame(t, srv, validV3Manifest("gui-added-game"))
	if code != http.StatusCreated {
		t.Fatalf("game must still register in-memory; status = %d", code)
	}
	if persisted, _ := out["persisted"].(bool); persisted {
		t.Fatalf("persisted must be false when persistence fails: %v", out)
	}
	if m, ok := out["manifest"].(map[string]any); !ok || m["game_id"] != "gui-added-game" {
		t.Fatalf("manifest echo missing: %v", out)
	}
}

func TestCatalogSaveToFileRejectsUnsafeIDs(t *testing.T) {
	cat := catalog.New()
	dir := t.TempDir()
	for _, id := range []string{"", "..", "a/b", "a\\b", ".hidden", "sp ace", strings.Repeat("x", 65)} {
		if err := cat.SaveToFile(id, dir); err == nil {
			t.Fatalf("SaveToFile accepted unsafe id %q", id)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("unsafe ids wrote files: %v", entries)
	}
}
