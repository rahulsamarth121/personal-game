package unit

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/personal-game/personal-game/internal/agent/acquire"
	"github.com/personal-game/personal-game/pkg/protocol"
)

// fixtureServer serves named blobs with Range support (ServeContent).
func fixtureServer(t *testing.T, blobs map[string][]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := filepath.Base(r.URL.Path)
		b, ok := blobs[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(b))
	}))
}

func shaOf(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// mkzip builds a zip with unix modes (0755 for setup.exe so extraction
// preserves executability, like 7-Zip does).
func mkzip(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, data := range files {
		hdr := &zip.FileHeader{Name: name, Method: zip.Deflate}
		hdr.SetMode(0o644)
		if name == "setup.exe" {
			hdr.SetMode(0o755)
		}
		fw, err := w.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func testDirs(t *testing.T) acquire.Dirs {
	t.Helper()
	base := t.TempDir()
	d := acquire.Dirs{
		Download: filepath.Join(base, "cache", "downloads"),
		Extract:  filepath.Join(base, "cache", "extract"),
		Games:    filepath.Join(base, "games"),
		State:    filepath.Join(base, "state"),
	}
	if err := d.Ensure(); err != nil {
		t.Fatal(err)
	}
	// Sentinel: saves-adjacent data the pipeline must never touch.
	if err := os.MkdirAll(filepath.Join(base, "saves"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "saves", "sentinel"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	return d
}

func loadExample(t *testing.T, name string) protocol.GameManifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "games", "examples", name))
	if err != nil {
		t.Fatal(err)
	}
	var m protocol.GameManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("%s invalid: %v", name, err)
	}
	return m
}

func repointSources(m protocol.GameManifest, srv *httptest.Server, blobs map[string][]byte) protocol.GameManifest {
	for i, s := range m.Acquisition.Sources {
		b := blobs[s.Filename]
		m.Acquisition.Sources[i].URL = srv.URL + "/" + s.Filename
		m.Acquisition.Sources[i].SHA256 = shaOf(b)
		m.Acquisition.Sources[i].SizeBytes = uint64(len(b))
	}
	return m
}

func TestExampleManifestsValid(t *testing.T) {
	for _, f := range []string{
		"case1-multipart-installer.json", "case2-prebuilt-zip.json",
		"case3-prebuilt-7z.json", "case4-direct-exe.json",
		"case5-archive-installer.json", "example-game.json",
	} {
		loadExample(t, f)
	}
}

func TestPrebuiltZipEndToEnd(t *testing.T) {
	gameZip := mkzip(t, map[string][]byte{
		"game.exe":       []byte("fake-exe"),
		"data/level.dat": []byte("level"),
	})
	blobs := map[string][]byte{"prebuilt-game.zip": gameZip}
	srv := fixtureServer(t, blobs)
	defer srv.Close()

	m := repointSources(loadExample(t, "case2-prebuilt-zip.json"), srv, blobs)
	m.GameID = "case2test"
	d := testDirs(t)
	dl := &acquire.Downloader{}
	p, err := acquire.Prepare(t.Context(), d, m, dl, 1<<40, 0)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if p.State != protocol.PipeReady {
		t.Fatalf("expected READY, got %s (%s)", p.State, p.Error)
	}
	root, err := acquire.GameRoot(d, m)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"game.exe", "data/level.dat"} {
		if _, err := os.Stat(filepath.Join(root, f)); err != nil {
			t.Fatalf("game file missing %s: %v", f, err)
		}
	}
	// Stage-gated cleanup: source archive gone, game + saves sentinel kept.
	if _, err := os.Stat(filepath.Join(d.Download, "prebuilt-game.zip")); !os.IsNotExist(err) {
		t.Fatal("source archive should be deleted after validated READY")
	}
}

func TestPrebuiltCleanupOptOut(t *testing.T) {
	gameZip := mkzip(t, map[string][]byte{"game.exe": []byte("fake-exe")})
	blobs := map[string][]byte{"prebuilt-game.zip": gameZip}
	srv := fixtureServer(t, blobs)
	defer srv.Close()

	m := repointSources(loadExample(t, "case2-prebuilt-zip.json"), srv, blobs)
	m.GameID = "case2keep"
	m.PackagePipeline.Prebuilt.ExpectedFiles = []string{"game.exe"}
	m.Cleanup.DeleteArchiveAfterPrebuiltReady = false
	d := testDirs(t)
	p, err := acquire.Prepare(t.Context(), d, m, &acquire.Downloader{}, 1<<40, 0)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if p.State != protocol.PipeReady {
		t.Fatalf("expected READY, got %s", p.State)
	}
	if _, err := os.Stat(filepath.Join(d.Download, "prebuilt-game.zip")); err != nil {
		t.Fatal("source archive must be RETAINED when policy opts out")
	}
}

func TestDirectExeEndToEnd(t *testing.T) {
	exe := []byte("tiny-fake-exe")
	blobs := map[string][]byte{"tiny-game.exe": exe}
	srv := fixtureServer(t, blobs)
	defer srv.Close()

	m := repointSources(loadExample(t, "case4-direct-exe.json"), srv, blobs)
	m.GameID = "case4test"
	d := testDirs(t)
	p, err := acquire.Prepare(t.Context(), d, m, &acquire.Downloader{}, 1<<40, 0)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if p.State != protocol.PipeReady {
		t.Fatalf("expected READY, got %s (%s)", p.State, p.Error)
	}
	root, _ := acquire.GameRoot(d, m)
	got, err := os.ReadFile(filepath.Join(root, "tiny-game.exe"))
	if err != nil || string(got) != string(exe) {
		t.Fatalf("placed exe wrong: %v", err)
	}
}

func TestInstallerCaseEndToEnd(t *testing.T) {
	// The "installer" is a copy of this very test binary run with a
	// self-listing flag (exits 0 fast, hermetic, cross-platform).
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	selfBytes, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	bundle := mkzip(t, map[string][]byte{"setup.exe": selfBytes})
	blobs := map[string][]byte{"setup-bundle.zip": bundle}
	srv := fixtureServer(t, blobs)
	defer srv.Close()

	m := repointSources(loadExample(t, "case5-archive-installer.json"), srv, blobs)
	m.GameID = "case5test"
	m.PackagePipeline.Installer.Arguments = []string{"-test.list", ".*"}
	m.PackagePipeline.Installer.ExpectedDir = "Case5"
	d := testDirs(t)
	// Simulate installer output so expected-dir/launch verification has
	// something real to check (the plumbing under test, not a real install).
	if err := os.MkdirAll(filepath.Join(d.Games, "case5test", "Case5"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.Games, "case5test", "game.exe"), []byte("installed"), 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := acquire.Prepare(t.Context(), d, m, &acquire.Downloader{}, 1<<40, 0)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if p.State != protocol.PipeReady {
		t.Fatalf("expected READY, got %s (%s)", p.State, p.Error)
	}
	// Installer staging cleaned; installed game kept.
	if _, err := os.Stat(filepath.Join(d.Extract, "case5test")); !os.IsNotExist(err) {
		t.Fatal("installer staging should be deleted after validated install")
	}
	if _, err := os.Stat(filepath.Join(d.Games, "case5test", "Case5")); err != nil {
		t.Fatal("installed game must be kept as warm cache")
	}
}

func TestMultipartVerifyAndOrder(t *testing.T) {
	a, b := []byte("part-one-data"), []byte("part-two-data")
	blobs := map[string][]byte{"game.part01.zip": a, "game.part02.zip": b}
	srv := fixtureServer(t, blobs)
	defer srv.Close()

	m := repointSources(loadExample(t, "case1-multipart-installer.json"), srv, blobs)
	d := testDirs(t)
	dl := &acquire.Downloader{}
	for _, s := range acquire.OrderedSources(m) {
		if err := dl.Fetch(t.Context(), d.Download, s); err != nil {
			t.Fatalf("fetch part %d: %v", s.Part, err)
		}
	}
	ordered := acquire.OrderedSources(m)
	if ordered[0].Part != 1 || ordered[1].Part != 2 {
		t.Fatalf("part order wrong: %+v", ordered)
	}
	if err := acquire.VerifyAll(d.Download, m); err != nil {
		t.Fatalf("verify: %v", err)
	}
	// Corrupt one part: verification must catch it.
	if err := os.WriteFile(filepath.Join(d.Download, "game.part02.zip"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := acquire.VerifyAll(d.Download, m); err == nil {
		t.Fatal("tampered part must fail verification")
	}
}

func TestDiskPlannerPerType(t *testing.T) {
	c1 := loadExample(t, "case1-multipart-installer.json")
	req1 := c1.Footprint.RequiredFor(acquire.EffectiveType(c1))
	want1 := uint64(42949672960 + 42949672960 + 53687091200 + 5368709120 + 47244640256 + 2147483648)
	if req1 != want1 {
		t.Fatalf("case1 requirement wrong: got %d want %d", req1, want1)
	}
	c2 := loadExample(t, "case2-prebuilt-zip.json")
	req2 := c2.Footprint.RequiredFor(acquire.EffectiveType(c2))
	if req2 != uint64(10737418240+10737418240+11811160064+1073741824) {
		t.Fatalf("case2 requirement wrong: got %d", req2)
	}
	c4 := loadExample(t, "case4-direct-exe.json")
	req4 := c4.Footprint.RequiredFor(acquire.EffectiveType(c4))
	if req4 != uint64(67108864+67108864+67108864) {
		t.Fatalf("case4 must not budget extraction peak: got %d", req4)
	}
	if !(req1 > req2 && req2 > req4) {
		t.Fatalf("expected installer > prebuilt > direct, got %d %d %d", req1, req2, req4)
	}
	// Abort message names the shortfall.
	r := acquire.CheckDiskFor(c1, 10, 0)
	if r.Fits || r.FitsAfterEvict || r.MissingBytes == 0 || r.Message == "" {
		t.Fatalf("expected clean abort: %+v", r)
	}
}

func TestManifestV3Rules(t *testing.T) {
	base := loadExample(t, "case2-prebuilt-zip.json")
	// Unknown mode rejected.
	bad := base
	bad.Acquisition.PackageType = "teleport"
	if err := bad.Validate(); err == nil {
		t.Fatal("unknown package_type must be rejected")
	}
	// Prebuilt must not invent an installer.
	bad = base
	bad.PackagePipeline.Installer = &protocol.InstallerSpec{Path: "setup.exe"}
	if err := bad.Validate(); err == nil {
		t.Fatal("prebuilt + installer must be rejected")
	}
	// Single-link modes require exactly one source.
	bad = base
	bad.Acquisition.Sources = append(append([]protocol.AcquisitionSource{}, base.Acquisition.Sources...),
		protocol.AcquisitionSource{URL: "https://example.invalid/x.zip", Part: 2, Filename: "x.zip"})
	if err := bad.Validate(); err == nil {
		t.Fatal("two sources in single-link mode must be rejected")
	}
	// Installer modes require the installer spec.
	bad = loadExample(t, "case5-archive-installer.json")
	bad.PackagePipeline.Installer = nil
	if err := bad.Validate(); err == nil {
		t.Fatal("installer mode without installer spec must be rejected")
	}
	// v3 archive without explicit mode rejected; v1/v2 still valid.
	bad = base
	bad.Acquisition.PackageType = ""
	if err := bad.Validate(); err == nil {
		t.Fatal("v3 archive without package_type must be rejected")
	}
	old := loadExample(t, "example-game.json")
	if old.SchemaVersion != 2 {
		t.Fatalf("legacy example should stay v2, got %d", old.SchemaVersion)
	}
	if acquire.EffectiveType(old) != protocol.PackageArchiveInstaller {
		t.Fatalf("legacy v2 + installer should map to archive_installer")
	}
	// Detection helper sanity (manifest stays authoritative).
	if protocol.DetectPackageType("game.iso") != protocol.PackageISOInstaller ||
		protocol.DetectPackageType("setup.exe") != protocol.PackageDirectPrebuilt ||
		protocol.DetectPackageType("g.zip") != protocol.PackageArchivePrebuilt ||
		protocol.DetectPackageType("README") != "" {
		t.Fatal("detection helper wrong")
	}
}

func TestLaunchTraversalRejected(t *testing.T) {
	d := testDirs(t)
	evil := protocol.GameManifest{
		SchemaVersion: 3, GameID: "evil", Name: "evil", Version: "1",
		Acquisition: protocol.AcquisitionRef{Provider: "archive", PackageType: protocol.PackageDirectPrebuilt,
			Sources: []protocol.AcquisitionSource{{URL: "u", Part: 1, Filename: "e.exe"}}},
		Launch: protocol.LaunchSpec{Executable: "../escape.exe"},
	}
	root, err := acquire.GameRoot(d, evil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := acquire.ValidatePrebuilt(root, evil); err == nil {
		t.Fatal("launch target escaping the game root must be rejected")
	}
	// Zip-slip blocked at extraction: no file may escape the dest dir.
	dest := t.TempDir()
	slipZip := mkzip(t, map[string][]byte{"../../evil.txt": []byte("x")})
	slipPath := filepath.Join(t.TempDir(), "slip.zip")
	if err := os.WriteFile(slipPath, slipZip, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := acquire.ExtractZip(slipPath, dest); err == nil {
		t.Fatal("zip traversal must be rejected")
	}
}
