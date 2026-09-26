package unit

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	agentSaves "github.com/personal-game/personal-game/internal/agent/saves"
	"github.com/personal-game/personal-game/pkg/protocol"
)

func saveManifest(gameID, liveDir string) protocol.GameManifest {
	return protocol.GameManifest{
		SchemaVersion: 3, GameID: gameID, Name: gameID, Version: "1",
		Acquisition: protocol.AcquisitionRef{Provider: "archive", PackageType: protocol.PackageArchivePrebuilt,
			Sources: []protocol.AcquisitionSource{{URL: "u", Part: 1, Filename: "g.zip"}}},
		Launch: protocol.LaunchSpec{Executable: "g.exe"},
		Saves: protocol.SaveSpec{Provider: "ludusavi", Overrides: []protocol.SaveOverride{
			{Platform: runtime.GOOS, Paths: []string{liveDir}},
		}},
	}
}

func writeSave(t *testing.T, dir, name, data string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSavePackageRoundTrip(t *testing.T) {
	base := t.TempDir()
	live := filepath.Join(base, "live", "game-saves")
	writeSave(t, live, "slot1.sav", "progress-1")
	writeSave(t, filepath.Join(live, "sub"), "cfg.ini", "settings")

	zipPath := filepath.Join(base, "snap.zip")
	sha, size, files, err := agentSaves.Package([]string{live}, zipPath)
	if err != nil {
		t.Fatalf("package: %v", err)
	}
	if len(sha) != 64 || size == 0 || files != 2 {
		t.Fatalf("bad package meta: %s %d %d", sha, size, files)
	}
	// Restore OVER a live tree with the SAME basename: atomic swap replaces
	// the live dir with the staged tree (blob layout is stage/<basename>).
	liveParent := filepath.Join(base, "live2")
	live2 := filepath.Join(liveParent, "game-saves")
	writeSave(t, live2, "stale.sav", "old")
	if err := agentSaves.Restore([]string{live2}, filepath.Join(base, "staging-dl"), "mem://nope", sha); err == nil {
		t.Fatal("mem:// must be refused by the http path (use object store in prod)")
	}
	// Exercise the verify -> stage -> swap path over the packaged blob
	// (transport covered by control tests with the memory object store).
	if err := agentSaves.RestoreFromBlob(t.TempDir(), []string{live2}, zipPath, sha); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(live2, "stale.sav")); !os.IsNotExist(err) {
		t.Fatal("stale live content must be replaced")
	}
	got, err := os.ReadFile(filepath.Join(live2, "slot1.sav"))
	if err != nil || string(got) != "progress-1" {
		t.Fatalf("restored content wrong: %v %q", err, got)
	}
	got, err = os.ReadFile(filepath.Join(live2, "sub", "cfg.ini"))
	if err != nil || string(got) != "settings" {
		t.Fatalf("restored subdir content wrong: %v %q", err, got)
	}
}

func TestSaveCorruptBlobKeepsLive(t *testing.T) {
	base := t.TempDir()
	live := filepath.Join(base, "live")
	writeSave(t, live, "slot1.sav", "keep-me")
	badZip := filepath.Join(base, "bad.zip")
	if err := os.WriteFile(badZip, []byte("not-a-zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := agentSaves.RestoreFromBlob(t.TempDir(), []string{live}, badZip,
		"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
	if err == nil {
		t.Fatal("corrupt blob must fail restore")
	}
	got, err := os.ReadFile(filepath.Join(live, "slot1.sav"))
	if err != nil || string(got) != "keep-me" {
		t.Fatal("live saves must survive a failed restore untouched")
	}
}

func TestSaveQuiescence(t *testing.T) {
	live := filepath.Join(t.TempDir(), "live")
	writeSave(t, live, "a.sav", "x")
	if err := agentSaves.WaitQuiescent([]string{live}, 100*time.Millisecond, 5*time.Second); err != nil {
		t.Fatalf("stable dir should pass: %v", err)
	}
	// Actively written dir must time out, not snapshot torn files.
	// Writes land every 50ms (like periodic game autosaves); quiescence
	// needs 400ms of calm that never comes before the 2s deadline.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				_ = os.WriteFile(filepath.Join(live, "a.sav"), []byte("churn"), 0o644)
			}
		}
	}()
	if err := agentSaves.WaitQuiescent([]string{live}, 400*time.Millisecond, 2*time.Second); err == nil {
		t.Fatal("churning dir must time out")
	}
}

func TestSaveResolveDirs(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	inside := filepath.Join(home, "pg-test-saves")
	m := saveManifest("g1", inside)
	dirs, err := agentSaves.ResolveDirs(m)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(dirs) != 1 || dirs[0] != inside {
		t.Fatalf("wrong dirs: %+v", dirs)
	}
	// Traversal outside home rejected.
	m.Saves.Overrides[0].Paths = []string{filepath.Join(home, "..", "evil")}
	if _, err := agentSaves.ResolveDirs(m); err == nil {
		t.Fatal("save path escaping home must be rejected")
	}
	// No overrides: clear error, not silent empty.
	m.Saves.Overrides = nil
	if _, err := agentSaves.ResolveDirs(m); err == nil {
		t.Fatal("missing save locations must fail clearly")
	}
}
