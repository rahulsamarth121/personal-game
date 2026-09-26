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
	"time"
)

// WaitQuiescent polls directory mtimes until no file changed for `stable`
// (debounce) or timeout expires. Snapshots must never capture torn writes.
func WaitQuiescent(dirs []string, stable, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	// Seed with now: quiescence means a FULL stable window with no change,
	// even when the files were already old at entry.
	lastChange := time.Now()
	for {
		changed, err := newestMtime(dirs)
		if err != nil {
			return err
		}
		now := time.Now()
		if changed.After(lastChange) {
			lastChange = changed
		}
		if now.Sub(lastChange) >= stable {
			return nil
		}
		if now.After(deadline) {
			return fmt.Errorf("saves: files still changing after %s (game still writing?)", timeout)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func newestMtime(dirs []string) (time.Time, error) {
	var newest time.Time
	for _, d := range dirs {
		err := filepath.Walk(d, func(_ string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // tolerate transient reader races; next poll retries
			}
			if info.ModTime().After(newest) {
				newest = info.ModTime()
			}
			return nil
		})
		if err != nil {
			return time.Time{}, err
		}
	}
	return newest, nil
}

// Package zips live save dirs into destZip (deterministic entry order not
// required; sha covers content) and returns sha256 + size + file count.
func Package(dirs []string, destZip string) (sha string, size uint64, files int, err error) {
	tmp := destZip + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return "", 0, 0, err
	}
	h := sha256.New()
	zw := zip.NewWriter(io.MultiWriter(out, h))
	count := 0
	for _, d := range dirs {
		base := filepath.Base(d)
		err := filepath.Walk(d, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(d, path)
			if err != nil {
				return err
			}
			name := filepath.ToSlash(filepath.Join(base, rel))
			if info.IsDir() {
				_, err := zw.Create(name + "/")
				return err
			}
			fw, err := zw.Create(name)
			if err != nil {
				return err
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			if _, err := io.Copy(fw, f); err != nil {
				return err
			}
			count++
			return nil
		})
		if err != nil {
			zw.Close()
			out.Close()
			os.Remove(tmp)
			return "", 0, 0, err
		}
	}
	if err := zw.Close(); err != nil {
		out.Close()
		os.Remove(tmp)
		return "", 0, 0, err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return "", 0, 0, err
	}
	st, err := os.Stat(tmp)
	if err != nil {
		return "", 0, 0, err
	}
	if err := os.Rename(tmp, destZip); err != nil {
		os.Remove(tmp)
		return "", 0, 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), uint64(st.Size()), count, nil
}

// Snapshot packages live saves after quiescence and PUTs the blob to the
// scoped upload URL. Returns blob metadata for the commit call.
func Snapshot(dirs []string, stagingZip, uploadURL string, stable, timeout time.Duration) (sha string, size uint64, files int, err error) {
	if err := WaitQuiescent(dirs, stable, timeout); err != nil {
		return "", 0, 0, err
	}
	sha, size, files, err = Package(dirs, stagingZip)
	if err != nil {
		return "", 0, 0, err
	}
	if err := UploadPUT(uploadURL, stagingZip, sha); err != nil {
		return "", 0, 0, err
	}
	return sha, size, files, nil
}

// UploadPUT streams a file to a presigned URL (works for http(s) and the
// mem:// test scheme is handled by callers via object.MemoryStore).
func UploadPUT(uploadURL, filePath, sha string) error {
	if strings.HasPrefix(uploadURL, "mem://") {
		return fmt.Errorf("saves: mem:// upload must go through object.MemoryStore")
	}
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPut, uploadURL, f)
	if err != nil {
		return err
	}
	req.ContentLength = st.Size()
	req.Header.Set("Content-Type", "application/zip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("saves: upload failed: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("saves: upload status %d", resp.StatusCode)
	}
	return nil
}
