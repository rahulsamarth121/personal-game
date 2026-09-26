package saves

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/personal-game/personal-game/internal/agent/proc"
)

// Ludusavi mode selection. Ludusavi is the save-location KNOWLEDGE source:
// `backup`/`restore` locate and copy game state; our generation/fencing
// pipeline still owns ordering, persistence, and authority. Deliberately
// NOT using `ludusavi wrap` (restore-launch-backup in one): it would hide
// the game process from our supervisor and bypass session integration.
//
// Verified CLI (mtkennerly/ludusavi docs/cli.md):
//
//	ludusavi backup --force --path <dir> <title>
//	ludusavi restore --force --path <dir> <title>
const (
	ludusaviBackupTimeout  = 10 * time.Minute
	ludusaviRestoreTimeout = 10 * time.Minute
)

// SaveMode classifies how saves are handled for reporting.
type SaveMode string

const (
	// SaveModeLudusavi uses the ludusavi binary (REAL when present).
	SaveModeLudusavi SaveMode = "ludusavi"
	// SaveModeOverrides uses manifest override paths directly.
	SaveModeOverrides SaveMode = "overrides"
	// SaveModeNone means no save locations are configured.
	SaveModeNone SaveMode = "none"
)

// HasLudusavi reports whether the binary is available.
func HasLudusavi() bool {
	_, err := exec.LookPath("ludusavi")
	return err == nil
}

// ModeFor selects the save mode: ludusavi title + binary wins, manifest
// overrides second, none when nothing is configured. Never silent.
func ModeFor(ludusaviTitle string, hasOverrides bool) (SaveMode, string) {
	if ludusaviTitle != "" {
		if HasLudusavi() {
			return SaveModeLudusavi, "ludusavi title " + ludusaviTitle
		}
		if hasOverrides {
			return SaveModeOverrides, "ludusavi title set but binary missing; using overrides"
		}
		return SaveModeNone, "ludusavi title set but binary missing and no overrides"
	}
	if hasOverrides {
		return SaveModeOverrides, "manifest overrides"
	}
	return SaveModeNone, "no ludusavi title and no overrides"
}

// BackupTo runs `ludusavi backup --force --path stage title` and verifies
// the stage actually received content (exit code alone is not proof).
func BackupTo(ctx context.Context, title, stageDir string) error {
	if title == "" {
		return fmt.Errorf("saves: empty ludusavi title")
	}
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return err
	}
	res := proc.Run(ctx, "ludusavi",
		[]string{"backup", "--force", "--path", stageDir, title}, "", ludusaviBackupTimeout)
	if res.TimedOut {
		return fmt.Errorf("saves: ludusavi backup timed out")
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("saves: ludusavi backup exit %d: %s", res.ExitCode, tailStr(res.Stderr, 2000))
	}
	nonEmpty, err := dirHasFiles(stageDir)
	if err != nil {
		return err
	}
	if !nonEmpty {
		return fmt.Errorf("saves: ludusavi backup produced no files for %q (wrong title?)", title)
	}
	return nil
}

// RestoreFrom runs `ludusavi restore --force --path stage title`.
func RestoreFrom(ctx context.Context, title, stageDir string) error {
	if title == "" {
		return fmt.Errorf("saves: empty ludusavi title")
	}
	res := proc.Run(ctx, "ludusavi",
		[]string{"restore", "--force", "--path", stageDir, title}, "", ludusaviRestoreTimeout)
	if res.TimedOut {
		return fmt.Errorf("saves: ludusavi restore timed out")
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("saves: ludusavi restore exit %d: %s", res.ExitCode, tailStr(res.Stderr, 2000))
	}
	return nil
}

// SnapshotLudusavi backs up via Ludusavi into staging, then packages with
// our pipeline (sha/size feed the commit call). Returns blob metadata.
func SnapshotLudusavi(ctx context.Context, title, stagingBase, stagingZip string) (sha string, size uint64, files int, err error) {
	stage := filepath.Join(stagingBase, "ludusavi")
	os.RemoveAll(stage)
	if err := BackupTo(ctx, title, stage); err != nil {
		return "", 0, 0, err
	}
	// Quiescence already enforced by the caller before snapshot; Ludusavi
	// just ran, so package immediately.
	return Package([]string{stage}, stagingZip)
}

// RestoreLudusavi verifies the blob, unpacks to staging, then restores via
// Ludusavi into the live locations Ludusavi knows.
func RestoreLudusavi(stagingBase, blobPath, wantSHA, title string) error {
	if err := verifyFileSHA(blobPath, wantSHA); err != nil {
		return err
	}
	stage := filepath.Join(stagingBase, "ludusavi")
	os.RemoveAll(stage)
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return err
	}
	if err := unzipTo(blobPath, stage); err != nil {
		return err
	}
	return RestoreFrom(context.Background(), title, stage)
}

func dirHasFiles(dir string) (bool, error) {
	found := false
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found, err
}

func tailStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
