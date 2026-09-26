package unit

import (
	"path/filepath"
	"testing"

	"github.com/personal-game/personal-game/internal/agent/cache"
	"github.com/personal-game/personal-game/internal/common"
	"github.com/personal-game/personal-game/pkg/protocol"
)

func TestValidatePathBlocksTraversal(t *testing.T) {
	root := filepath.Join("testdata", "games")
	_ = root
	allowed := []string{"/srv/games"}
	if _, err := common.ValidatePath(allowed, "/srv/games/doom/game.exe"); err != nil {
		t.Fatalf("inside-root path rejected: %v", err)
	}
	if _, err := common.ValidatePath(allowed, "/srv/games/../../etc/passwd"); err == nil {
		t.Fatal("traversal outside root must be rejected")
	}
	if _, err := common.ValidatePath(allowed, "/etc/passwd"); err == nil {
		t.Fatal("absolute escape must be rejected")
	}
	if _, err := common.ValidatePath(nil, "/srv/games/x"); err == nil {
		t.Fatal("empty allowlist must reject everything")
	}
}

func TestDiskPlan(t *testing.T) {
	fp := protocol.Footprint{DownloadBytes: 10, PeakTempBytes: 10, InstalledBytes: 20, SafetyHeadroom: 5}
	p := common.PlanDisk(fp, 100, 0)
	if !p.Fits || p.RequiredBytes != 45 {
		t.Fatalf("expected fit with required=45, got %+v", p)
	}
	p = common.PlanDisk(fp, 10, 0)
	if p.Fits || p.FitsAfterEvict {
		t.Fatalf("expected clean abort, got %+v", p)
	}
	p = common.PlanDisk(fp, 10, 100)
	if p.Fits || !p.FitsAfterEvict {
		t.Fatalf("expected fit-after-evict, got %+v", p)
	}
}

func TestCacheEvictionProtectsActive(t *testing.T) {
	entries := []cache.Entry{
		{GameID: "doom", Bytes: 50},
		{GameID: "quake", Bytes: 30, Protected: true},
		{GameID: "hexen", Bytes: 20},
	}
	if got := cache.EvictableBytes(entries, "doom"); got != 20 {
		t.Fatalf("expected 20 evictable (hexen only), got %d", got)
	}
}

func TestManifestValidation(t *testing.T) {
	m := protocol.GameManifest{
		SchemaVersion: 1, // v1 stays valid without sources
		GameID:        "doom2", Name: "DOOM II",
		Acquisition: protocol.AcquisitionRef{Provider: "archive"},
		Launch:      protocol.LaunchSpec{Executable: "doom2.exe"},
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	bad := m
	bad.Launch.Executable = ""
	if err := bad.Validate(); err == nil {
		t.Fatal("manifest without executable must be rejected")
	}
}
