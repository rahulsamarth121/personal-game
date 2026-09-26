package unit

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/personal-game/personal-game/pkg/protocol"
)

func playBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "playbin")
	if os.Getenv("OS") == "Windows" || filepath.Separator == '\\' {
		bin += ".exe"
	}
	// Tests run with cwd=tests/unit; the package path is module-relative.
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", bin, "./cmd/play")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build play: %v %s", err, out)
	}
	return bin
}

func runAddGame(t *testing.T, bin, outDir string, args ...string) (string, int) {
	t.Helper()
	full := append([]string{"add-game", "--out", filepath.Join(outDir, "m.json")}, args...)
	cmd := exec.Command(bin, full...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	return string(out), code
}

func loadGen(t *testing.T, outDir string) protocol.GameManifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(outDir, "m.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m protocol.GameManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("generated manifest invalid: %v", err)
	}
	return m
}

func TestAddGameModes(t *testing.T) {
	bin := playBinary(t)
	cases := []struct {
		name  string
		args  []string
		check func(t *testing.T, m protocol.GameManifest)
	}{
		{"multipart-installer", []string{
			"--game-id", "g1", "--package-type", "archive_installer",
			"--url", "https://example.invalid/p1.zip",
			"--url", "https://example.invalid/p2.zip",
			"--url", "https://example.invalid/p3.zip",
			"--exe", "game.exe", "--iso-result", "game.iso", "--iso-extract",
			"--installer", "setup.exe",
		}, func(t *testing.T, m protocol.GameManifest) {
			if len(m.Acquisition.Sources) != 3 || m.Acquisition.Sources[2].Part != 3 {
				t.Fatalf("3 ordered parts: %+v", m.Acquisition.Sources)
			}
			if m.Acquisition.Sources[0].Filename != "p1.zip" {
				t.Fatalf("basename default: %+v", m.Acquisition.Sources[0])
			}
			if !m.Cleanup.DeletePartsAfterArchiveExtract || !m.Cleanup.DeleteISOAfterExtract {
				t.Fatalf("cleanup: %+v", m.Cleanup)
			}
		}},
		{"prebuilt-zip", []string{
			"--game-id", "g2", "--package-type", "archive_prebuilt",
			"--url", "https://example.invalid/game.zip",
			"--sha256", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"--size", "1234", "--exe", "game.exe", "--expected-file", "game.exe",
		}, func(t *testing.T, m protocol.GameManifest) {
			if len(m.Acquisition.Sources) != 1 || m.Acquisition.Sources[0].SHA256 == "" {
				t.Fatalf("single checksummed source: %+v", m.Acquisition.Sources)
			}
			if m.PackagePipeline.Installer != nil {
				t.Fatal("prebuilt must not invent an installer")
			}
		}},
		{"direct-exe", []string{
			"--game-id", "g3", "--package-type", "direct_prebuilt",
			"--url", "https://example.invalid/tiny.exe", "--exe", "tiny.exe",
		}, func(t *testing.T, m protocol.GameManifest) {
			if m.PackagePipeline.Archive != nil {
				t.Fatal("direct exe takes no archive stage")
			}
		}},
		{"single-installer", []string{
			"--game-id", "g4", "--package-type", "archive_installer",
			"--url", "https://example.invalid/bundle.zip", "--exe", "game.exe",
			"--installer", "setup.exe",
		}, func(t *testing.T, m protocol.GameManifest) {
			if m.PackagePipeline.ISO != nil {
				t.Fatal("no ISO declared means no ISO stage")
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if out, code := runAddGame(t, bin, dir, tc.args...); code != 0 {
				t.Fatalf("exit %d: %s", code, out)
			}
			tc.check(t, loadGen(t, dir))
		})
	}
}

func TestAddGameRejects(t *testing.T) {
	bin := playBinary(t)
	// Unknown mode.
	if _, code := runAddGame(t, bin, t.TempDir(), "--game-id", "x",
		"--package-type", "teleport", "--url", "https://example.invalid/a.zip", "--exe", "g.exe"); code == 0 {
		t.Fatal("unknown mode must fail")
	}
	// Prebuilt + installer is incoherent.
	if _, code := runAddGame(t, bin, t.TempDir(), "--game-id", "x",
		"--package-type", "archive_prebuilt", "--url", "https://example.invalid/a.zip",
		"--exe", "g.exe", "--installer", "setup.exe"); code == 0 {
		t.Fatal("prebuilt+installer must fail")
	}
	// Two sources in single-link mode.
	if _, code := runAddGame(t, bin, t.TempDir(), "--game-id", "x",
		"--package-type", "direct_prebuilt", "--url", "https://example.invalid/a.exe",
		"--url", "https://example.invalid/b.exe", "--exe", "a.exe"); code == 0 {
		t.Fatal("two sources in single-link mode must fail")
	}
	// Refuse overwrite without --force.
	dir := t.TempDir()
	base := []string{"--game-id", "x", "--package-type", "direct_prebuilt",
		"--url", "https://example.invalid/a.exe", "--exe", "a.exe"}
	if _, code := runAddGame(t, bin, dir, base...); code != 0 {
		t.Fatal("first write must succeed")
	}
	if _, code := runAddGame(t, bin, dir, base...); code == 0 {
		t.Fatal("overwrite without --force must fail")
	}
}
