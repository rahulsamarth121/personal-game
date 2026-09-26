package acquire

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/personal-game/personal-game/internal/agent/proc"
	"github.com/personal-game/personal-game/internal/common"
	"github.com/personal-game/personal-game/pkg/protocol"
)

// SteamCMD provider: installs via the legitimate SteamCMD tool into the
// game cache. Only anonymous or user-owned logins are supported — the
// provider never bypasses Steam authentication, DRM, or licensing; without
// valid credentials/entitlements Steam itself refuses.
//
// Manifest reference fields (acquisition.reference):
//
//	app_id (required): Steam app id (number or string)
//	username (optional): login name, default "anonymous"
//	validate (optional): pass validate to app_update
//	timeout_secs (optional): install timeout, default 2h
//
// Live execution needs the steamcmd binary + network + entitlements; unit
// tests cover arg building and failure paths without them.
const defaultSteamTimeout = 2 * time.Hour

// SteamCMDProvider implements AcquisitionProvider via steamcmd.
type SteamCMDProvider struct {
	AppID      string
	Username   string
	Validate   bool
	Timeout    time.Duration
	InstallDir string
}

// SteamCMDConfig returns the provider for a manifest, or a clear error
// (missing app_id, install dir escaping the games root).
func SteamCMDConfig(m protocol.GameManifest, gamesDir string) (*SteamCMDProvider, error) {
	ref := m.Acquisition.Reference
	appID := refString(ref, "app_id")
	if appID == "" || appID == "0" {
		return nil, fmt.Errorf("acquire: steamcmd requires acquisition.reference.app_id")
	}
	user := refString(ref, "username")
	if user == "" {
		user = "anonymous"
	}
	installDir, err := common.ValidatePath([]string{gamesDir}, filepath.Join(gamesDir, m.GameID))
	if err != nil {
		return nil, fmt.Errorf("acquire: games root invalid: %w", err)
	}
	timeout := defaultSteamTimeout
	if v := refString(ref, "timeout_secs"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			timeout = time.Duration(n) * time.Second
		}
	}
	validate := refString(ref, "validate") == "true"
	return &SteamCMDProvider{AppID: appID, Username: user, Validate: validate,
		Timeout: timeout, InstallDir: installDir}, nil
}

func refString(ref map[string]any, key string) string {
	if ref == nil {
		return ""
	}
	switch v := ref[key].(type) {
	case string:
		return v
	case float64:
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
		return ""
	case bool:
		if v {
			return "true"
		}
		return ""
	default:
		return ""
	}
}

// Name implements AcquisitionProvider.
func (p *SteamCMDProvider) Name() string { return "steamcmd" }

// Args builds the fixed steamcmd argv (no shell): login, install dir,
// app update, quit.
func (p *SteamCMDProvider) Args() []string {
	args := []string{"+login", p.Username, "+force_install_dir", p.InstallDir,
		"+app_update", p.AppID}
	if p.Validate {
		args = append(args, "validate")
	}
	return append(args, "+quit")
}

// Fetch implements AcquisitionProvider: runs steamcmd and verifies the
// install dir actually received content (exit code alone is not proof).
// Reruns are cheap: a non-empty install dir verifies without reinstalling,
// which also keeps multi-source manifests to a single install.
func (p *SteamCMDProvider) Fetch(ctx context.Context, _ string, s protocol.AcquisitionSource) error {
	_ = s
	if nonEmpty, err := dirHasContent(p.InstallDir); err == nil && nonEmpty {
		return nil
	}
	res := proc.Run(ctx, "steamcmd", p.Args(), "", p.Timeout)
	if res.ExitCode != 0 {
		if res.ExitCode == -1 {
			return fmt.Errorf("acquire: steamcmd not installed or not runnable (see docs/operations/local.md): %s",
				tail([]byte(res.Stderr), 500))
		}
		return fmt.Errorf("acquire: steamcmd exit %d: %s", res.ExitCode, tail([]byte(res.Stderr), 2000))
	}
	nonEmpty, err := dirHasContent(p.InstallDir)
	if err != nil {
		return err
	}
	if !nonEmpty {
		return fmt.Errorf("acquire: steamcmd finished but install dir is empty (login/entitlement issue?)")
	}
	return nil
}

func dirHasContent(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return len(entries) > 0, nil
}
