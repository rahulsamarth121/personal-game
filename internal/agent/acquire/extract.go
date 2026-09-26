package acquire

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/personal-game/personal-game/internal/agent/proc"
	"github.com/personal-game/personal-game/internal/common"
	"github.com/personal-game/personal-game/pkg/protocol"
)

func partPath(dir string, s protocol.AcquisitionSource) string {
	return filepath.Join(dir, s.Filename)
}

// Has7z reports whether 7-Zip is available for archive/ISO handling.
func Has7z() bool {
	for _, n := range []string{"7z", "7zz"} {
		if _, err := exec.LookPath(n); err == nil {
			return true
		}
	}
	return false
}

// sevenZip extracts src into destDir via 7-Zip (handles zip/rar/7z/iso).
// Fixed argv, enforced timeout, verified exit code.
func sevenZip(ctx context.Context, src, destDir string) error {
	bin := "7z"
	if _, err := exec.LookPath(bin); err != nil {
		bin = "7zz"
	}
	res := proc.Run(ctx, bin, []string{"x", src, "-o" + destDir, "-y"}, "", 0)
	if res.ExitCode != 0 {
		return fmt.Errorf("7z extract failed (exit %d): %s", res.ExitCode, tail([]byte(res.Stderr), 2000))
	}
	return nil
}

// ExtractZip stdlib fallback for plain zip archives (used when 7z is absent
// and in tests). Every entry is validated against destDir: no traversal.
func ExtractZip(src, destDir string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()
	allowed := []string{destDir}
	for _, f := range zr.File {
		dest := filepath.Join(destDir, filepath.FromSlash(f.Name))
		clean, verr := common.ValidatePath(allowed, dest)
		if verr != nil {
			return fmt.Errorf("zip traversal blocked: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(clean, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(clean), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		// Preserve stored unix modes (like 7-Zip does); default to 0644.
		// Without this, executables extracted on Linux lose +x.
		mode := os.FileMode(0o644)
		if stored := f.FileHeader.ExternalAttrs >> 16; stored != 0 {
			mode = os.FileMode(stored & 0o777)
		}
		out, err := os.OpenFile(clean, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		rc.Close()
		out.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// ExtractArchive reconstructs/extracts the archive stage. For a single zip
// part it extracts directly; multipart sets are extracted via 7z (which
// resolves the full set from the first part). Result must contain the
// manifest's expected result path when declared.
func ExtractArchive(ctx context.Context, d Dirs, m protocol.GameManifest) error {
	sources := OrderedSources(m)
	if len(sources) == 0 {
		return fmt.Errorf("extract: no sources")
	}
	stage := filepath.Join(d.Extract, m.GameID)
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return err
	}
	first := partPath(d.Download, sources[0])
	archType := "zip"
	if m.PackagePipeline != nil && m.PackagePipeline.Archive != nil && m.PackagePipeline.Archive.Type != "" {
		archType = strings.ToLower(m.PackagePipeline.Archive.Type)
	}
	if len(sources) == 1 && archType == "zip" && !Has7z() {
		if err := ExtractZip(first, stage); err != nil {
			return err
		}
	} else {
		if !Has7z() {
			return fmt.Errorf("extract: multipart/%s archives require 7-Zip", archType)
		}
		if err := sevenZip(ctx, first, stage); err != nil {
			return err
		}
	}
	if m.PackagePipeline != nil && m.PackagePipeline.Result != nil && m.PackagePipeline.Result.Path != "" {
		want := filepath.Join(stage, m.PackagePipeline.Result.Path)
		if _, err := os.Stat(want); err != nil {
			return fmt.Errorf("extract: expected result %q missing: %w", m.PackagePipeline.Result.Path, err)
		}
	}
	return nil
}

// ExtractISO extracts the ISO into the installer staging dir via 7z.
// ISOs are only handled by extraction, never mounted+executed blindly.
func ExtractISO(ctx context.Context, isoPath, destDir string) error {
	if !Has7z() {
		return fmt.Errorf("iso extract requires 7-Zip")
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	return sevenZip(ctx, isoPath, destDir)
}

// FindResult locates the pipeline result (e.g. game.iso) under stage.
func FindResult(stage string, want protocol.ResultSpec) (string, error) {
	p := filepath.Join(stage, want.Path)
	st, err := os.Stat(p)
	if err != nil {
		return "", fmt.Errorf("result %q not found: %w", want.Path, err)
	}
	if st.IsDir() && want.Type == "iso" {
		return "", fmt.Errorf("result %q is a directory, expected ISO file", want.Path)
	}
	return p, nil
}
