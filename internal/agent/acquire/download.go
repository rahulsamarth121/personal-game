package acquire

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/personal-game/personal-game/pkg/protocol"
)

// Downloader fetches one source part with resume, retries, and progress.
type Downloader struct {
	// UseAria2c prefers orchestrating aria2c when installed; the internal
	// HTTP downloader is the fallback (and what tests exercise).
	UseAria2c bool
	// MaxRetries caps attempts (0 = default 3, negative = single attempt).
	MaxRetries int
	Progress   func(part int, done, total uint64)
	HTTP       *http.Client
}

// Aria2cArgs builds the fixed argv used to orchestrate aria2c for one part:
// resume (-c), retries, concurrency per part, and explicit output name.
// No shell is involved; the caller execs this argv directly.
func Aria2cArgs(url, outDir, filename string, connections int) []string {
	if connections < 1 {
		connections = 4
	}
	return []string{
		"--continue=true",
		"--max-tries=10",
		"--retry-wait=5",
		"--split=" + strconv.Itoa(connections),
		"--max-connection-per-server=" + strconv.Itoa(connections),
		"--dir=" + outDir,
		"--out=" + filename,
		"--auto-file-renaming=false",
		"--allow-overwrite=false",
		url,
	}
}

// HasAria2c reports whether the mature downloader is available.
func HasAria2c() bool {
	_, err := exec.LookPath("aria2c")
	return err == nil
}

// Fetch downloads one part into dir, resuming partial files and verifying
// size when the manifest declares it. Cancel via ctx.
func (d *Downloader) Fetch(ctx context.Context, dir string, s protocol.AcquisitionSource) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	retries := d.MaxRetries
	if retries == 0 {
		retries = 3
	} else if retries < 0 {
		retries = 0
	}
	var last error
	for attempt := 0; attempt <= retries; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if isFileURL(s.URL) {
			// Local files copy outright (no resume needed); still retried
			// and size-checked like every other source.
			last = d.fetchFile(dir, s)
		} else if d.UseAria2c && HasAria2c() {
			last = d.fetchAria2c(ctx, dir, s)
		} else {
			last = d.fetchHTTP(ctx, dir, s)
		}
		if last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 2 * time.Second):
		}
	}
	return fmt.Errorf("acquire: part %d failed after %d attempts: %w", s.Part, retries+1, last)
}

func (d *Downloader) fetchAria2c(ctx context.Context, dir string, s protocol.AcquisitionSource) error {
	args := Aria2cArgs(s.URL, dir, s.Filename, 4)
	cmd := exec.CommandContext(ctx, "aria2c", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("aria2c part %d: %v: %s", s.Part, err, tail(out, 2000))
	}
	return d.checkSize(filepath.Join(dir, s.Filename), s)
}

// fetchHTTP is the resume-capable fallback: Range requests append to the
// partial file, so interrupted downloads recover instead of restarting.
func (d *Downloader) fetchHTTP(ctx context.Context, dir string, s protocol.AcquisitionSource) error {
	dest := filepath.Join(dir, s.Filename)
	var resumeFrom int64
	if st, err := os.Stat(dest); err == nil {
		resumeFrom = st.Size()
	}
	if s.SizeBytes > 0 && uint64(resumeFrom) == s.SizeBytes {
		return nil // already complete
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return err
	}
	if resumeFrom > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", resumeFrom))
	}
	client := d.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("http part %d: status %d", s.Part, resp.StatusCode)
	}
	flag := os.O_CREATE | os.O_WRONLY
	if resp.StatusCode == http.StatusPartialContent {
		flag |= os.O_APPEND
	} else {
		flag |= os.O_TRUNC
		resumeFrom = 0
	}
	f, err := os.OpenFile(dest, flag, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	done := uint64(resumeFrom)
	buf := make([]byte, 256*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			done += uint64(n)
			if d.Progress != nil {
				d.Progress(s.Part, done, s.SizeBytes)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	return d.checkSize(dest, s)
}

func (d *Downloader) checkSize(dest string, s protocol.AcquisitionSource) error {
	if s.SizeBytes == 0 {
		return nil
	}
	st, err := os.Stat(dest)
	if err != nil {
		return err
	}
	if uint64(st.Size()) != s.SizeBytes {
		return fmt.Errorf("part %d size mismatch: got %d, want %d", s.Part, st.Size(), s.SizeBytes)
	}
	return nil
}

func tail(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[len(b)-n:])
}
