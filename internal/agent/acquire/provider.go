package acquire

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/personal-game/personal-game/pkg/protocol"
)

// AcquisitionProvider is the acquisition abstraction: each source kind knows
// fetch one part. ArchiveProvider (aria2c/HTTP/file) is fully implemented;
// store providers (steamcmd/legendary/gog tooling) plug in here without
// touching the pipeline, disk gate, or cleanup stages.
type AcquisitionProvider interface {
	// Name identifies the provider ("archive", "steamcmd", ...).
	Name() string
	// Fetch downloads one part into dir with resume/retry/verify semantics.
	Fetch(ctx context.Context, dir string, s protocol.AcquisitionSource) error
}

// ArchiveProvider fetches URL/file parts via Downloader.
type ArchiveProvider struct {
	DL *Downloader
}

// Name implements Provider.
func (p *ArchiveProvider) Name() string { return "archive" }

// Fetch implements Provider.
func (p *ArchiveProvider) Fetch(ctx context.Context, dir string, s protocol.AcquisitionSource) error {
	dl := p.DL
	if dl == nil {
		dl = &Downloader{}
	}
	return dl.Fetch(ctx, dir, s)
}

// ProviderFor resolves the manifest's provider name. Unknown names fail
// loudly; provider-managed installers (steamcmd etc.) are explicit future
// work, not silent skips.
func ProviderFor(name string, dl *Downloader) (AcquisitionProvider, error) {
	switch name {
	case "", "archive":
		return &ArchiveProvider{DL: dl}, nil
	default:
		return nil, fmt.Errorf("acquire: provider %q not implemented (archive is; steamcmd/legendary/gog are future work)", name)
	}
}

// ProviderForManifest resolves the manifest's provider, including
// manifest-parameterized ones (steamcmd needs app_id + install dir).
func ProviderForManifest(m protocol.GameManifest, dl *Downloader, gamesDir string) (AcquisitionProvider, error) {
	if m.Acquisition.Provider == "steamcmd" {
		return SteamCMDConfig(m, gamesDir)
	}
	return ProviderFor(m.Acquisition.Provider, dl)
}

// isFileURL reports whether a source is a local file (user-supplied game
// content on disk). The rest of the pipeline (verify/extract/validate/
// cleanup) treats it identically to a download.
func isFileURL(raw string) bool {
	return strings.HasPrefix(strings.ToLower(raw), "file://")
}

// fileURLPath converts a file:// URL to an OS path (percent-decoded,
// Windows drive aware).
func fileURLPath(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("acquire: bad file URL: %w", err)
	}
	p, err := url.PathUnescape(u.Path)
	if err != nil {
		return "", fmt.Errorf("acquire: bad file URL encoding: %w", err)
	}
	if runtime.GOOS == "windows" {
		p = strings.TrimPrefix(p, "/")
	}
	return filepath.FromSlash(p), nil
}

// fetchFile copies a local source into the download dir (with size check
// and progress reporting, matching the network paths' contract).
func (d *Downloader) fetchFile(dir string, s protocol.AcquisitionSource) error {
	src, err := fileURLPath(s.URL)
	if err != nil {
		return err
	}
	st, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("acquire: local source part %d not readable %q: %w", s.Part, src, err)
	}
	if st.IsDir() {
		return fmt.Errorf("acquire: local source part %d is a directory %q (point at the file)", s.Part, src)
	}
	dest := filepath.Join(dir, s.Filename)
	if same, _ := sameFile(src, dest); same {
		return d.checkSize(dest, s)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	var done uint64
	buf := make([]byte, 256*1024)
	for {
		n, rerr := in.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
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
			out.Close()
			return fmt.Errorf("acquire: read local source: %w", rerr)
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	return d.checkSize(dest, s)
}

func sameFile(a, b string) (bool, error) {
	afa, err := filepath.Abs(a)
	if err != nil {
		return false, err
	}
	bfa, err := filepath.Abs(b)
	if err != nil {
		return false, err
	}
	return afa == bfa, nil
}
