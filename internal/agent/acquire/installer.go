package acquire

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/personal-game/personal-game/internal/agent/proc"
	"github.com/personal-game/personal-game/internal/common"
	"github.com/personal-game/personal-game/pkg/protocol"
)

// DefaultInstallerTimeout bounds installer execution when the manifest
// declares none.
const DefaultInstallerTimeout = 2 * time.Hour

// RunInstaller executes the manifest's installer under strict controls:
// path must resolve inside stageDir, fixed argv (no shell), enforced
// timeout, captured exit code, and expected-dir verification afterwards.
// Only installers the user is authorized to use are ever launched, and only
// from files the pipeline itself produced or verified.
func RunInstaller(ctx context.Context, stageDir string, spec protocol.InstallerSpec, gamesDir, gameID string) error {
	if spec.Path == "" {
		return fmt.Errorf("installer: empty path")
	}
	allowed := []string{stageDir}
	instPath, err := common.ValidatePath(allowed, filepath.Join(stageDir, spec.Path))
	if err != nil {
		return fmt.Errorf("installer: path escapes staging: %w", err)
	}
	if _, err := os.Stat(instPath); err != nil {
		return fmt.Errorf("installer: not found: %w", err)
	}
	timeout := time.Duration(spec.TimeoutSecs) * time.Second
	if timeout <= 0 {
		timeout = DefaultInstallerTimeout
	}
	res := proc.Run(ctx, instPath, spec.Arguments, stageDir, timeout)
	if res.TimedOut {
		return fmt.Errorf("installer: timed out after %s", timeout)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("installer: exit %d: %s", res.ExitCode, tail([]byte(res.Stderr+"\n"+res.Stdout), 2000))
	}
	// Verify the expected installation before any cleanup.
	if spec.ExpectedDir != "" {
		installed, verr := common.ValidatePath([]string{gamesDir}, filepath.Join(gamesDir, gameID))
		if verr != nil {
			return fmt.Errorf("installer: games root invalid: %w", verr)
		}
		want := filepath.Join(installed, spec.ExpectedDir)
		if _, err := os.Stat(want); err != nil {
			return fmt.Errorf("installer: expected dir %q missing after install: %w", spec.ExpectedDir, err)
		}
	}
	return nil
}

// ValidateLaunchTarget confirms the manifest's executable exists under the
// installed game dir so PLAY never starts a missing binary.
func ValidateLaunchTarget(gamesDir, gameID string, launch protocol.LaunchSpec) (string, error) {
	gameRoot, err := common.ValidatePath([]string{gamesDir}, filepath.Join(gamesDir, gameID))
	if err != nil {
		return "", err
	}
	exe, err := common.ValidatePath([]string{gameRoot}, filepath.Join(gameRoot, launch.Executable))
	if err != nil {
		return "", fmt.Errorf("launch target escapes game dir: %w", err)
	}
	if _, err := os.Stat(exe); err != nil {
		return "", fmt.Errorf("launch target missing: %w", err)
	}
	return exe, nil
}
