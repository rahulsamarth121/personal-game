package acquire

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/personal-game/personal-game/internal/common"
	"github.com/personal-game/personal-game/pkg/protocol"
)

// EffectiveType resolves the preparation mode. v3 package_type is
// authoritative; v1/v2 manifests predate modes and are interpreted from
// their declared stages (installer => archive_installer, ISO => iso_installer,
// otherwise archive_prebuilt). Detection by filename is only a tiebreaker
// when a v3 manifest omits stages it should have declared — validation
// rejects incoherent manifests before we ever get here.
func EffectiveType(m protocol.GameManifest) protocol.PackageType {
	if m.SchemaVersion >= 3 && m.Acquisition.PackageType != "" {
		return m.Acquisition.PackageType
	}
	if m.Acquisition.Provider != "archive" {
		return protocol.PackageProvider
	}
	if m.PackagePipeline != nil {
		if m.PackagePipeline.Installer != nil {
			return protocol.PackageArchiveInstaller
		}
		if m.PackagePipeline.ISO != nil {
			return protocol.PackageISOInstaller
		}
	}
	return protocol.PackageArchivePrebuilt
}

// GameRoot resolves the validated game directory: /games/<id>[/GameRoot].
// Everything playable lives under it; saves live elsewhere entirely.
func GameRoot(d Dirs, m protocol.GameManifest) (string, error) {
	root, err := common.ValidatePath([]string{d.Games}, filepath.Join(d.Games, m.GameID))
	if err != nil {
		return "", fmt.Errorf("acquire: games root invalid: %w", err)
	}
	if m.PackagePipeline != nil && m.PackagePipeline.Prebuilt != nil &&
		m.PackagePipeline.Prebuilt.GameRoot != "" {
		sub := m.PackagePipeline.Prebuilt.GameRoot
		if filepath.IsAbs(sub) {
			return "", fmt.Errorf("acquire: prebuilt game_root must be relative")
		}
		root, err = common.ValidatePath([]string{root}, filepath.Join(root, sub))
		if err != nil {
			return "", fmt.Errorf("acquire: prebuilt game_root escapes game dir: %w", err)
		}
	}
	return root, nil
}

// isArchive reports whether the file needs extraction (by extension).
func isArchive(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".zip") || strings.HasSuffix(lower, ".rar") ||
		strings.HasSuffix(lower, ".7z")
}

// PlacePrebuilt turns one verified download into a playable game:
// archives are extracted into the game root, a direct EXE is copied under
// the manifest's launch name. The launch target plus expected files are
// validated before anything is reported READY. No installer ever runs here.
func PlacePrebuilt(ctx context.Context, d Dirs, m protocol.GameManifest, src protocol.AcquisitionSource) (string, error) {
	_ = ctx
	downloaded := partPath(d.Download, src)
	if _, err := os.Stat(downloaded); err != nil {
		return "", fmt.Errorf("prebuilt: source missing: %w", err)
	}
	root, err := GameRoot(d, m)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	if isArchive(src.Filename) {
		if strings.HasSuffix(strings.ToLower(src.Filename), ".zip") && !Has7z() {
			if err := ExtractZip(downloaded, root); err != nil {
				return "", err
			}
		} else {
			if !Has7z() {
				return "", fmt.Errorf("prebuilt: %s archives require 7-Zip", src.Filename)
			}
			if err := sevenZip(ctx, downloaded, root); err != nil {
				return "", err
			}
		}
	} else {
		// Single file (e.g. direct EXE): copy under the launch name so the
		// on-disk layout always matches the manifest, whatever the host
		// chose to call the download.
		dest := filepath.Join(root, filepath.Base(m.Launch.Executable))
		clean, verr := common.ValidatePath([]string{root}, dest)
		if verr != nil {
			return "", verr
		}
		if err := copyFile(downloaded, clean); err != nil {
			return "", err
		}
	}
	if err := ValidatePrebuilt(root, m); err != nil {
		return "", err
	}
	return root, nil
}

// ValidatePrebuilt confirms the launch target exists inside the game root
// plus every manifest-declared expected file. Nothing is executed.
func ValidatePrebuilt(root string, m protocol.GameManifest) error {
	exe, verr := common.ValidatePath([]string{root}, filepath.Join(root, m.Launch.Executable))
	if verr != nil {
		return fmt.Errorf("prebuilt: launch target escapes game root: %w", verr)
	}
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf("prebuilt: launch target missing: %w", err)
	}
	if m.PackagePipeline != nil && m.PackagePipeline.Prebuilt != nil {
		for _, want := range m.PackagePipeline.Prebuilt.ExpectedFiles {
			p, verr := common.ValidatePath([]string{root}, filepath.Join(root, want))
			if verr != nil {
				return fmt.Errorf("prebuilt: expected file escapes game root: %w", verr)
			}
			if _, err := os.Stat(p); err != nil {
				return fmt.Errorf("prebuilt: expected file %q missing: %w", want, err)
			}
		}
	}
	return nil
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := out.ReadFrom(in); err != nil {
		return err
	}
	return out.Close()
}
