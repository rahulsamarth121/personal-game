package main

// add-game: guided manifest generator emitting valid schema-v3 JSON.
// No binaries, no scraping, no dashboard — one command, one file:
//
//	go run ./cmd/play add-game --game-id doom2 --name "DOOM II" \
//	  --package-type archive_prebuilt --url https://example.invalid/g.zip \
//	  --exe game.exe
//
// Repeatable flags (--url, --filename, --sha256, --size, --expected-file,
// --save-path, --arg, --installer-arg) pair by index; --filename defaults
// to the URL basename and parts number from 1 in order. The manifest is
// validated before writing and never overwritten without --force.

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/personal-game/personal-game/pkg/protocol"
)

func cmdAddGame(args []string) int {
	fs := flag.NewFlagSet("add-game", flag.ExitOnError)
	gameID := fs.String("game-id", "", "game id, e.g. doom2 (required)")
	name := fs.String("name", "", "display name (default: game id)")
	version := fs.String("version", "1.0", "game version")
	provider := fs.String("provider", "archive", "acquisition provider")
	packageType := fs.String("package-type", "", "provider|archive_installer|archive_prebuilt|direct_prebuilt|iso_installer (required)")
	urls := repeatable(fs, "url", "source URL (repeat for multipart, any N)")
	filenames := repeatable(fs, "filename", "explicit filename per source (default: URL basename)")
	shas := repeatable(fs, "sha256", "checksum per source (recommended)")
	sizes := repeatable(fs, "size", "size in bytes per source")
	archiveType := fs.String("archive-type", "zip", "zip|rar|7z (archive modes)")
	isoResult := fs.String("iso-result", "", "expected ISO path after archive extract (installer modes)")
	isoExtract := fs.Bool("iso-extract", false, "extract the ISO after reconstruction")
	installer := fs.String("installer", "", "installer path inside staging (installer modes)")
	installerArgs := repeatable(fs, "installer-arg", "fixed installer argument (repeatable)")
	installerTimeout := fs.Int("installer-timeout", 3600, "installer timeout in seconds")
	installerDir := fs.String("installer-expected-dir", "", "expected installed directory name")
	exe := fs.String("exe", "", "launch executable, e.g. game.exe (required)")
	launchArgs := repeatable(fs, "arg", "launch argument (repeatable)")
	workdir := fs.String("workdir", ".", "working directory")
	wolfApp := fs.String("wolf-app", "", "Wolf application title (Wolf backend)")
	gameRoot := fs.String("game-root", "", "prebuilt subdir under the game dir")
	expectedFiles := repeatable(fs, "expected-file", "required file for prebuilt validation (repeatable)")
	ludusaviTitle := fs.String("ludusavi-title", "", "Ludusavi game title for saves")
	savePaths := repeatable(fs, "save-path", "save location override, %VAR%/~/$VAR expanded (repeatable)")
	osName := fs.String("os", "windows", "runtime os")
	downloadBytes := fs.Uint64("download-bytes", 0, "footprint hint: download size")
	installedBytes := fs.Uint64("installed-bytes", 0, "footprint hint: installed size")
	headroom := fs.Uint64("headroom-bytes", 0, "footprint hint: safety headroom")
	out := fs.String("out", "", "output path (default games/manifests/<id>.json)")
	force := fs.Bool("force", false, "overwrite existing manifest")
	_ = fs.Parse(args)

	if *gameID == "" || *packageType == "" || *exe == "" {
		fmt.Fprintln(os.Stderr, "add-game: --game-id, --package-type and --exe are required")
		return 2
	}
	pt := protocol.PackageType(*packageType)
	if !pt.IsValid() {
		fmt.Fprintln(os.Stderr, "add-game: unknown package-type (see --help for values)")
		return 2
	}
	if len(*urls) == 0 {
		fmt.Fprintln(os.Stderr, "add-game: at least one --url is required")
		return 2
	}
	sources, err := buildSources(*urls, *filenames, *shas, *sizes)
	if err != nil {
		fmt.Fprintln(os.Stderr, "add-game:", err)
		return 2
	}
	m := protocol.GameManifest{
		SchemaVersion: protocol.ManifestSchemaVersion,
		GameID:        *gameID,
		Name:          firstNonEmpty(*name, *gameID),
		Version:       *version,
		Acquisition: protocol.AcquisitionRef{
			Provider: *provider, PackageType: pt, Sources: sources,
		},
		Footprint: protocol.Footprint{
			DownloadBytes: *downloadBytes, InstalledBytes: *installedBytes,
			SafetyHeadroom: *headroom,
		},
		Runtime: protocol.RuntimeSpec{OS: *osName},
		Launch: protocol.LaunchSpec{
			Executable: *exe, Arguments: *launchArgs,
			WorkingDirectory: *workdir, WolfApp: *wolfApp,
		},
		Saves: protocol.SaveSpec{Provider: "ludusavi", LudusaviTitle: *ludusaviTitle},
	}
	if len(*savePaths) > 0 {
		m.Saves.Overrides = []protocol.SaveOverride{{Platform: "any", Paths: *savePaths}}
	}
	pipe := &protocol.PackagePipeline{}
	if pt.IsPrebuilt() && *installer != "" {
		fmt.Fprintln(os.Stderr, "add-game: prebuilt modes take no --installer (a prebuilt package is already the game)")
		return 2
	}
	switch pt {
	case protocol.PackageArchiveInstaller, protocol.PackageISOInstaller:
		if *installer == "" {
			fmt.Fprintln(os.Stderr, "add-game: installer modes require --installer")
			return 2
		}
		pipe.Archive = &protocol.ArchiveSpec{Type: *archiveType, Multipart: len(sources) > 1}
		if *isoResult != "" || *isoExtract {
			if *isoResult == "" {
				fmt.Fprintln(os.Stderr, "add-game: --iso-extract needs --iso-result")
				return 2
			}
			pipe.Result = &protocol.ResultSpec{Type: "iso", Path: *isoResult}
			pipe.ISO = &protocol.IsoSpec{Extract: true}
		}
		pipe.Installer = &protocol.InstallerSpec{
			Type: "exe", Path: *installer, Arguments: *installerArgs,
			TimeoutSecs: *installerTimeout, ExpectedDir: *installerDir,
		}
		m.Cleanup = protocol.CleanupPolicy{
			DeletePartsAfterArchiveExtract: true,
			DeleteISOAfterExtract:          *isoExtract,
			DeleteInstallerAfterInstall:    true,
		}
	case protocol.PackageArchivePrebuilt:
		pipe.Archive = &protocol.ArchiveSpec{Type: *archiveType}
		pipe.Prebuilt = &protocol.PrebuiltSpec{GameRoot: *gameRoot, ExpectedFiles: *expectedFiles}
		m.Cleanup = protocol.CleanupPolicy{DeleteArchiveAfterPrebuiltReady: true}
	case protocol.PackageDirectPrebuilt:
		pipe.Prebuilt = &protocol.PrebuiltSpec{GameRoot: *gameRoot, ExpectedFiles: *expectedFiles}
		m.Cleanup = protocol.CleanupPolicy{DeleteArchiveAfterPrebuiltReady: true}
	case protocol.PackageProvider:
		// Provider-managed: no local pipeline stages.
		pipe = nil
	}
	m.PackagePipeline = pipe
	if err := m.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "add-game: generated manifest invalid:", err)
		return 1
	}
	dest := *out
	if dest == "" {
		root := repoRoot()
		dest = filepath.Join(root, "games", "manifests", *gameID+".json")
	}
	if _, err := os.Stat(dest); err == nil && !*force {
		fmt.Fprintln(os.Stderr, "add-game: refusing to overwrite", dest, "(use --force)")
		return 1
	}
	raw, _ := json.MarshalIndent(m, "", "  ")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "add-game:", err)
		return 1
	}
	if err := os.WriteFile(dest, append(raw, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "add-game:", err)
		return 1
	}
	fmt.Println("wrote", dest)
	if *downloadBytes == 0 || *installedBytes == 0 {
		fmt.Println("note: footprint sizes are 0 (unknown) — fill in real sizes so the disk gate can protect you")
	}
	return 0
}

// repeatable registers a string-slice flag.
func repeatable(fs *flag.FlagSet, name, usage string) *[]string {
	var v []string
	fs.Var(&strSlice{&v}, name, usage)
	return &v
}

type strSlice struct{ v *[]string }

func (s *strSlice) String() string       { return strings.Join(*s.v, ",") }
func (s *strSlice) Set(val string) error { *s.v = append(*s.v, val); return nil }

// buildSources pairs parallel flag lists by index (part = index+1).
func buildSources(urls, filenames, shas, sizes []string) ([]protocol.AcquisitionSource, error) {
	for _, other := range [][]string{filenames, shas, sizes} {
		if len(other) > 0 && len(other) != len(urls) {
			return nil, fmt.Errorf("mismatched counts: %d urls vs %d metadata entries", len(urls), len(other))
		}
	}
	var out []protocol.AcquisitionSource
	for i, u := range urls {
		if u == "" {
			return nil, fmt.Errorf("empty url at position %d", i+1)
		}
		name := ""
		if len(filenames) > 0 {
			name = filenames[i]
		}
		if name == "" {
			name = urlBasename(u)
		}
		if name == "" {
			return nil, fmt.Errorf("cannot infer filename for url %d (pass --filename)", i+1)
		}
		src := protocol.AcquisitionSource{URL: u, Part: i + 1, Filename: name}
		if len(shas) > 0 {
			src.SHA256 = shas[i]
		}
		if len(sizes) > 0 {
			var n uint64
			if _, err := fmt.Sscanf(sizes[i], "%d", &n); err != nil {
				return nil, fmt.Errorf("bad --size %q at position %d", sizes[i], i+1)
			}
			src.SizeBytes = n
		}
		out = append(out, src)
	}
	return out, nil
}

// urlBasename takes the final path segment of http(s)/file URLs.
func urlBasename(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Path != "" {
		if base := filepath.Base(filepath.FromSlash(u.Path)); base != "." && base != "/" {
			return base
		}
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// repoRoot finds the checkout root (dir containing go.mod), else cwd.
func repoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}
