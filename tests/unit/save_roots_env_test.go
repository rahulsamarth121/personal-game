package unit

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/personal-game/personal-game/internal/agent/saves"
	"github.com/personal-game/personal-game/internal/common"
)

func TestSaveRootsDefaultIsHomeOnly(t *testing.T) {
	t.Setenv("PG_SAVE_ROOTS", "")
	roots, err := saves.SaveRoots()
	if err != nil {
		t.Fatalf("SaveRoots: %v", err)
	}
	home, _ := os.UserHomeDir()
	if len(roots) != 1 || roots[0] != home {
		t.Fatalf("default roots = %v, want [%s]", roots, home)
	}
}

func TestSaveRootsParsesListAndSkipsEmpty(t *testing.T) {
	home, _ := os.UserHomeDir()
	sep := string(os.PathListSeparator)
	var root1, root2 string
	if runtime.GOOS == "windows" {
		root1 = `E:\pg-work`
		root2 = `C:\other work`
	} else {
		root1 = "/opt/pg-work"
		root2 = "/mnt/other work"
	}
	t.Setenv("PG_SAVE_ROOTS", root1+sep+root2+sep+"")
	roots, err := saves.SaveRoots()
	if err != nil {
		t.Fatalf("SaveRoots: %v", err)
	}
	want := []string{home, root1, root2}
	if len(roots) != len(want) {
		t.Fatalf("roots = %v, want %v", roots, want)
	}
	for i := range want {
		if roots[i] != want[i] {
			t.Fatalf("roots[%d] = %q, want %q", i, roots[i], want[i])
		}
	}
}

func TestSaveRootsEnvAllowsTempVolumeSaves(t *testing.T) {
	t.Setenv("PG_SAVE_ROOTS", os.TempDir())
	roots, err := saves.SaveRoots()
	if err != nil {
		t.Fatalf("SaveRoots: %v", err)
	}
	target := filepath.Join(os.TempDir(), "some-game", "save.bin")
	if _, err := common.ValidatePath(roots, target); err != nil {
		t.Fatalf("save under declared temp root rejected: %v", err)
	}
}
