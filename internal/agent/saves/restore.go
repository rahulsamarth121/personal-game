package saves

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/personal-game/personal-game/internal/common"
)

// Restore downloads a save blob to temp, verifies its checksum, unpacks to
// staging, and atomically swaps each live dir (rename live->backup,
// stage->live, drop backup; rollback on any failure). A partial download
// never touches a live save directory.
func Restore(liveDirs []string, stagingBase, downloadURL, wantSHA string) error {
	roots, err := SaveRoots()
	if err != nil {
		return err
	}
	for _, d := range liveDirs {
		if _, verr := common.ValidatePath(roots, d); verr != nil {
			return fmt.Errorf("saves: live dir escapes approved roots: %w", verr)
		}
	}
	if err := os.MkdirAll(stagingBase, 0o755); err != nil {
		return err
	}
	tmp, err := downloadTemp(stagingBase, downloadURL)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	return RestoreFromBlob(stagingBase, liveDirs, tmp, wantSHA)
}

// RestoreFromBlob runs the verify -> stage -> swap path over an already
// downloaded blob. Tests drive this directly (hermetic); production calls
// Restore, which downloads first through the identical code path.
func RestoreFromBlob(stagingBase string, liveDirs []string, blobPath, wantSHA string) error {
	if err := verifyFileSHA(blobPath, wantSHA); err != nil {
		return err
	}
	stage := filepath.Join(stagingBase, "stage")
	os.RemoveAll(stage)
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return err
	}
	if err := unzipTo(blobPath, stage); err != nil {
		return err
	}
	return swapAll(liveDirs, stage)
}

// downloadTemp fetches the blob into the staging dir (never live).
func downloadTemp(stagingBase, downloadURL string) (string, error) {
	if strings.HasPrefix(downloadURL, "mem://") {
		return "", fmt.Errorf("saves: mem:// download must go through object.MemoryStore")
	}
	resp, err := http.Get(downloadURL)
	if err != nil {
		return "", fmt.Errorf("saves: download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("saves: download status %d", resp.StatusCode)
	}
	tmp, err := os.CreateTemp(stagingBase, "blob-*.tmp")
	if err != nil {
		return "", err
	}
	defer tmp.Close()
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}

func verifyFileSHA(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("saves: checksum mismatch (blob corrupt or tampered)")
	}
	return nil
}

// unzipTo extracts a save blob into stage. Every entry is validated
// against the stage dir (no zip-slip); the swap step additionally requires
// each staged tree to map onto a validated live dir, so a malicious blob
// cannot plant files outside the save tree.
func unzipTo(zipPath, stage string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("saves: unreadable blob: %w", err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		dest := filepath.Join(stage, filepath.FromSlash(f.Name))
		clean, verr := common.ValidatePath([]string{stage}, dest)
		if verr != nil {
			return fmt.Errorf("saves: blob entry escapes staging: %s", f.Name)
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
		if err := extractFile(f, clean); err != nil {
			return err
		}
	}
	return nil
}

func extractFile(f *zip.File, dest string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, rc)
	return err
}

// swapAll atomically replaces each live dir with its staged counterpart.
// The blob layout mirrors Package(): stage/<base-of-live-dir>/...
func swapAll(liveDirs []string, stage string) error {
	type swapped struct{ live, backup string }
	var done []swapped
	rollback := func() {
		for i := len(done) - 1; i >= 0; i-- {
			os.RemoveAll(done[i].live)
			os.Rename(done[i].backup, done[i].live)
		}
	}
	for _, live := range liveDirs {
		src := filepath.Join(stage, filepath.Base(live))
		if _, err := os.Stat(src); err != nil {
			rollback()
			return fmt.Errorf("saves: blob lacks saves for %s: %w", live, err)
		}
		backup := live + ".bak"
		os.RemoveAll(backup)
		if _, err := os.Stat(live); err == nil {
			if err := os.Rename(live, backup); err != nil {
				rollback()
				return fmt.Errorf("saves: cannot stage aside live dir: %w", err)
			}
		} else if err := os.MkdirAll(filepath.Dir(live), 0o755); err != nil {
			rollback()
			return fmt.Errorf("saves: cannot create save parent: %w", err)
		}
		if err := os.Rename(src, live); err != nil {
			rollback()
			return fmt.Errorf("saves: atomic swap failed (rolled back): %w", err)
		}
		done = append(done, swapped{live, backup})
	}
	for _, s := range done {
		os.RemoveAll(s.backup)
	}
	return nil
}
