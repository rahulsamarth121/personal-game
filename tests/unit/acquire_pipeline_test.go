package unit

import (
	"testing"

	"github.com/personal-game/personal-game/internal/agent/acquire"
	"github.com/personal-game/personal-game/internal/agent/cache"
	"github.com/personal-game/personal-game/pkg/protocol"
)

func TestPipelineHappyPath(t *testing.T) {
	path := []protocol.PipelineState{
		protocol.PipePlanned, protocol.PipeSpaceChecked, protocol.PipeDownloading,
		protocol.PipeDownloadVerified, protocol.PipeArchiveReady, protocol.PipeArchiveExtracted,
		protocol.PipeSourceCleaned, protocol.PipeISOReady, protocol.PipeISOExtracted,
		protocol.PipeISOCleaned, protocol.PipeInstallerReady, protocol.PipeInstalling,
		protocol.PipeInstallValidated, protocol.PipeInstallerCleaned, protocol.PipeReady,
	}
	for i := 0; i+1 < len(path); i++ {
		if !path[i].CanTransition(path[i+1]) {
			t.Fatalf("legal pipeline step %s -> %s rejected", path[i], path[i+1])
		}
	}
}

func TestPipelineSkipsAndFailure(t *testing.T) {
	// No-ISO manifest may jump SOURCE_CLEANED -> INSTALLER_READY; portable
	// games may jump DOWNLOAD_VERIFIED -> READY.
	for _, tr := range [][2]protocol.PipelineState{
		{protocol.PipeSourceCleaned, protocol.PipeInstallerReady},
		{protocol.PipeDownloadVerified, protocol.PipeReady},
		{protocol.PipeISOCleaned, protocol.PipeReady},
		{protocol.PipeDownloading, protocol.PipeFailed},
		{protocol.PipeFailed, protocol.PipePlanned},
	} {
		if !tr[0].CanTransition(tr[1]) {
			t.Fatalf("%s -> %s should be legal", tr[0], tr[1])
		}
	}
	if protocol.PipePlanned.CanTransition(protocol.PipeReady) {
		t.Fatal("PLANNED -> READY must be illegal (no skipping the pipeline)")
	}
	if protocol.PipeReady.CanTransition(protocol.PipeDownloading) {
		t.Fatal("READY is terminal (retry starts a new plan)")
	}
}

func TestManifestV2Validation(t *testing.T) {
	m := protocol.GameManifest{
		SchemaVersion: 2, GameID: "g", Name: "G", Version: "1",
		Acquisition: protocol.AcquisitionRef{Provider: "archive", Sources: []protocol.AcquisitionSource{
			{URL: "https://example.invalid/a.zip", Part: 2, Filename: "a.zip"},
			{URL: "https://example.invalid/b.zip", Part: 1, Filename: "b.zip"},
		}},
		Launch: protocol.LaunchSpec{Executable: "g.exe"},
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("valid v2 manifest rejected: %v", err)
	}
	if got := acquire.OrderedSources(m); got[0].Part != 1 || got[1].Part != 2 {
		t.Fatalf("sources not ordered by part: %+v", got)
	}
	dup := m
	dup.Acquisition.Sources[1].Part = 2
	if err := dup.Validate(); err == nil {
		t.Fatal("duplicate part numbers must be rejected")
	}
	// Stage 0 v1 manifests keep working.
	m.GameID = "old"
	m.SchemaVersion = 1
	m.Acquisition.Sources = nil
	if err := m.Validate(); err != nil {
		t.Fatalf("v1 manifest rejected: %v", err)
	}
}

func TestDiskGateMessages(t *testing.T) {
	fp := protocol.Footprint{DownloadBytes: 40, PeakTempBytes: 40, InstalledBytes: 50, SafetyHeadroom: 10}
	r := acquire.CheckDisk(fp, 1000, 0)
	if !r.Fits || r.MissingBytes != 0 {
		t.Fatalf("expected fit: %+v", r)
	}
	r = acquire.CheckDisk(fp, 10, 0)
	if r.Fits || r.FitsAfterEvict || r.MissingBytes == 0 || r.Message == "" {
		t.Fatalf("expected clean abort with reason: %+v", r)
	}
}

func TestEvictionLRUProtectsActive(t *testing.T) {
	entries := []cache.Entry{
		{GameID: "new", Bytes: 10, LastUsedUnix: 300},
		{GameID: "old", Bytes: 30, LastUsedUnix: 100},
		{GameID: "mid", Bytes: 30, LastUsedUnix: 200, Protected: true},
		{GameID: "active", Bytes: 999, LastUsedUnix: 50},
	}
	victims, ok := cache.EvictionPlan(entries, "active", 25)
	if !ok || len(victims) != 1 || victims[0].GameID != "old" {
		t.Fatalf("expected [old], got %+v ok=%v", victims, ok)
	}
	if _, ok := cache.EvictionPlan(entries, "active", 10000); ok {
		t.Fatal("uncoverable need must report false, never touch protected/active")
	}
}

func TestAria2cArgsNoShell(t *testing.T) {
	args := acquire.Aria2cArgs("https://example.invalid/a.zip", "/tmp/dl", "a.zip", 4)
	joined := ""
	for _, a := range args {
		joined += a + " "
	}
	for _, want := range []string{"--continue=true", "--dir=/tmp/dl", "--out=a.zip", "https://example.invalid/a.zip"} {
		found := false
		for _, a := range args {
			if a == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("argv missing %q in %q", want, joined)
		}
	}
}
