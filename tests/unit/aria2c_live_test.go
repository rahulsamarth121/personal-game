package unit

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/personal-game/personal-game/internal/agent/acquire"
	"github.com/personal-game/personal-game/pkg/protocol"
)

// TestAria2cLiveDownload runs the REAL aria2c binary against a local HTTP
// server when present (it is on this machine). No network beyond loopback.
// Skips honestly where aria2c is absent.
func TestAria2cLiveDownload(t *testing.T) {
	if !acquire.HasAria2c() {
		t.Skip("aria2c not installed")
	}
	payload := bytes.Repeat([]byte("aria2c-live-"), 32*1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "live.bin", time.Time{}, bytes.NewReader(payload))
	}))
	defer srv.Close()
	s := protocol.AcquisitionSource{URL: srv.URL + "/live.bin", Part: 1,
		Filename: "live.bin", SHA256: sha(payload), SizeBytes: uint64(len(payload))}
	dir := t.TempDir()
	dl := &acquire.Downloader{UseAria2c: true, MaxRetries: -1}
	if err := dl.Fetch(context.Background(), dir, s); err != nil {
		t.Fatalf("aria2c fetch: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "live.bin"))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatal("aria2c bytes differ")
	}
	if err := acquire.VerifyPart(filepath.Join(dir, "live.bin"), s); err != nil {
		t.Fatalf("verify: %v", err)
	}
}
