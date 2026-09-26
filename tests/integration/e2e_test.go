// Package integration holds end-to-end tests that run the REAL control
// plane HTTP API with REAL acquisition (file:// fixtures), REAL save
// blobs (HTTP object store), and REAL session/playtime accounting. The
// ONLY fake is the gaming backend (FakeBackend, explicitly MOCK):
// no Wolf/Sunshine exists in CI.
package integration

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/personal-game/personal-game/internal/agent/acquire"
	"github.com/personal-game/personal-game/internal/agent/backend"
	agentSession "github.com/personal-game/personal-game/internal/agent/session"
	"github.com/personal-game/personal-game/internal/control/api"
	"github.com/personal-game/personal-game/internal/control/catalog"
	"github.com/personal-game/personal-game/internal/control/nodes"
	controlSaves "github.com/personal-game/personal-game/internal/control/saves"
	csession "github.com/personal-game/personal-game/internal/control/session"
	"github.com/personal-game/personal-game/pkg/protocol"
)

// httpObjectStore is a REAL S3-shaped object store over HTTP (PUT stores,
// GET serves). No mocks: bytes cross the loopback interface.
type httpObjectStore struct {
	mu    sync.Mutex
	blobs map[string][]byte
	base  string
}

func (o *httpObjectStore) handler(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/obj/")
	o.mu.Lock()
	defer o.mu.Unlock()
	switch r.Method {
	case http.MethodPut:
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read", 500)
			return
		}
		o.blobs[key] = raw
		w.WriteHeader(200)
	case http.MethodGet:
		b, ok := o.blobs[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	default:
		http.Error(w, "method", 405)
	}
}

func (o *httpObjectStore) PresignUpload(_ context.Context, key, _ string, _ time.Duration) (string, error) {
	return o.base + "/obj/" + key, nil
}

func (o *httpObjectStore) PresignDownload(_ context.Context, key string, _ time.Duration) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.blobs[key]; !ok {
		return "", fmt.Errorf("not found")
	}
	return o.base + "/obj/" + key, nil
}

type world struct {
	t        *testing.T
	base     string
	dirs     acquire.Dirs
	liveDir  string
	manifest protocol.GameManifest
	cat      *catalog.Registry
	reg      *nodes.Registry
	sessMgr  *csession.Manager
	srv      *httptest.Server
	apiURL   string
	fake     *backend.FakeBackend
	token    map[string]uint64 // session -> fence token
	nodeID   string
}

func sha(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func mkzip(files map[string][]byte) []byte {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, data := range files {
		fw, err := w.Create(name)
		if err != nil {
			panic(err)
		}
		if _, err := fw.Write(data); err != nil {
			panic(err)
		}
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func setupWorld(t *testing.T) *world {
	t.Helper()
	base := t.TempDir()
	// Fixture game: zip with executable + data, served via file:// URL.
	gameZip := mkzip(map[string][]byte{"game.exe": []byte("exe"), "data/x.dat": []byte("x")})
	zipPath := filepath.Join(base, "game.zip")
	if err := os.WriteFile(zipPath, gameZip, 0o644); err != nil {
		t.Fatal(err)
	}
	fileURL := "file:///" + strings.TrimPrefix(filepath.ToSlash(zipPath), "/")
	liveDir := filepath.Join(base, "profile", "saves")
	m := protocol.GameManifest{
		SchemaVersion: 3, GameID: "e2e-game", Name: "E2E", Version: "1",
		Acquisition: protocol.AcquisitionRef{Provider: "archive", PackageType: protocol.PackageArchivePrebuilt,
			Sources: []protocol.AcquisitionSource{{URL: fileURL, Part: 1, Filename: "game.zip",
				SHA256: sha(gameZip), SizeBytes: uint64(len(gameZip))}}},
		Footprint:       protocol.Footprint{SafetyHeadroom: 1 << 20},
		PackagePipeline: &protocol.PackagePipeline{Prebuilt: &protocol.PrebuiltSpec{ExpectedFiles: []string{"game.exe"}}},
		Launch:          protocol.LaunchSpec{Executable: "game.exe"},
		Cleanup:         protocol.CleanupPolicy{DeleteArchiveAfterPrebuiltReady: true},
		Saves: protocol.SaveSpec{Provider: "ludusavi", Overrides: []protocol.SaveOverride{
			{Platform: "any", Paths: []string{liveDir}},
		}},
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("fixture manifest: %v", err)
	}
	// Seed catalog from a manifests dir (the real seeding path).
	mdir := filepath.Join(base, "manifests")
	if err := os.MkdirAll(mdir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(mdir, "e2e-game.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	cat := catalog.New()
	if _, err := cat.LoadDir(mdir); err != nil {
		t.Fatalf("seed: %v", err)
	}

	reg := nodes.New()
	sessMgr := csession.NewManager(csession.NewMemoryStore(), cat, reg)
	objs := &httpObjectStore{blobs: map[string][]byte{}}
	ots := httptest.NewServer(http.HandlerFunc(objs.handler))
	t.Cleanup(ots.Close)
	objs.base = ots.URL
	savesMgr := controlSaves.NewManager(controlSaves.NewMemoryStore(), objs, sessMgr)
	savesMgr.OnCommitted = sessMgr.NoteSaveGen
	srv := api.NewWithSessions(cat, reg, sessMgr)
	srv.Saves = savesMgr
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	// Enroll the node through the real API. Caps represent a genuinely
	// capable gaming node: policy bit, backend, usable media endpoint.
	enrollBody, _ := json.Marshal(map[string]any{"node_id": "e2e-node", "caps": map[string]any{
		"os": "linux", "arch": "amd64", "streaming_allowed": true,
		"wolf": true, "streaming": []string{"wolf"},
		"network": map[string]any{"media_network": map[string]any{
			"provider": "tailscale", "endpoint": "100.100.1.1",
			"address_family": "ipv4", "tcp_ok": true, "udp_ok": true, "reachable": true,
		}},
	}})
	resp, err := http.Post(ts.URL+"/v1/nodes/enroll", "application/json", bytes.NewReader(enrollBody))
	if err != nil {
		t.Fatal(err)
	}
	var enroll struct {
		FenceToken uint64 `json:"fence_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&enroll); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	dirs := acquire.Dirs{
		Download: filepath.Join(base, "data", "cache", "downloads"),
		Extract:  filepath.Join(base, "data", "cache", "extract"),
		Games:    filepath.Join(base, "data", "games"),
		State:    filepath.Join(base, "data", "state"),
	}
	return &world{t, base, dirs, liveDir, m, cat, reg, sessMgr, nil, ts.URL,
		backend.NewFakeBackend(), map[string]uint64{}, "e2e-node"}
}

func (w *world) createSession() *protocol.Session {
	w.t.Helper()
	raw, _ := json.Marshal(map[string]string{"user_id": "u1", "game_id": "e2e-game"})
	resp, err := http.Post(w.apiURL+"/v1/sessions", "application/json", bytes.NewReader(raw))
	if err != nil {
		w.t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		w.t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	var s protocol.Session
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		w.t.Fatal(err)
	}
	w.token[s.SessionID] = s.FenceToken
	return &s
}

func (w *world) getSession(id string) *protocol.Session {
	w.t.Helper()
	resp, err := http.Get(w.apiURL + "/v1/sessions/" + id)
	if err != nil {
		w.t.Fatal(err)
	}
	defer resp.Body.Close()
	var s protocol.Session
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		w.t.Fatal(err)
	}
	return &s
}

func (w *world) handler() *agentSession.Handler {
	return w.handlerFor(w.dirs, filepath.Join(w.base, "data", "staging"))
}

func (w *world) handlerFor(dirs acquire.Dirs, stagingBase string) *agentSession.Handler {
	return &agentSession.Handler{
		Control:         agentSession.NewControl(w.apiURL),
		Backend:         w.fake,
		Dirs:            dirs,
		SaveStaging:     stagingBase,
		Host:            "e2e-host",
		DiskFree:        func(string) (uint64, error) { return 1 << 40, nil },
		PollInterval:    100 * time.Millisecond,
		ReadyTimeout:    15 * time.Second,
		DisconnectGrace: 2 * time.Second,
	}
}

func (w *world) waitState(id string, want protocol.SessionState, timeout time.Duration) *protocol.Session {
	w.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		s := w.getSession(id)
		if s.State == want {
			return s
		}
		if s.State.IsTerminal() {
			w.t.Fatalf("session ended as %s while waiting for %s", s.State, want)
		}
		if time.Now().After(deadline) {
			w.t.Fatalf("timeout waiting for %s (still %s)", want, s.State)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestVerticalSlice runs catalog -> session -> prepare -> save/restore ->
// READY/STREAMING -> exit -> final save -> close -> playtime with a MOCK
// backend and REAL everything else, across three runs: cold cache, warm
// cache + restore, and full new-node rebuild (JOURNEY E).
func TestVerticalSlice(t *testing.T) {
	w := setupWorld(t)

	// RUN 1: cold cache (acquire for real) + first save.
	s1 := w.createSession()
	if s1.NodeID != "e2e-node" {
		t.Fatalf("node not assigned: %+v", s1)
	}
	done := make(chan error, 1)
	go func() {
		_, err := w.handler().Run(context.Background(), s1.SessionID, "e2e-node", s1.FenceToken)
		done <- err
	}()
	w.waitState(s1.SessionID, protocol.SessionStreaming, 30*time.Second)
	// The "game" writes a save mid-play; final snapshot must capture it.
	if err := os.MkdirAll(w.liveDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.liveDir, "slot1.sav"), []byte("progress-1"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	w.fake.Stop(backend.Game{Manifest: w.manifest, Host: "e2e-host"})
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run1: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("run1 did not finish")
	}
	s := w.getSession(s1.SessionID)
	if s.State != protocol.SessionClosed || s.EndReason != protocol.EndGameExit {
		t.Fatalf("run1 end wrong: %+v", s)
	}
	if len(s.SaveGens) != 1 || s.SaveGens[0] != 1 {
		t.Fatalf("run1 save gens wrong: %+v", s.SaveGens)
	}
	if s.Stream.Host != "e2e-host" || len(s.Stream.App) == 0 {
		t.Fatalf("stream info missing: %+v", s.Stream)
	}
	// Warm cache: game files present, source zip cleaned.
	if _, err := os.Stat(filepath.Join(w.dirs.Games, "e2e-game", "game.exe")); err != nil {
		t.Fatalf("installed game missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(w.dirs.Download, "game.zip")); !os.IsNotExist(err) {
		t.Fatal("source archive should be cleaned after READY")
	}

	// RUN 2: warm cache (no re-download) + restore gen1 + save gen2.
	// Wipe live saves to prove restore repopulates them.
	os.RemoveAll(w.liveDir)
	s2 := w.createSession()
	done2 := make(chan error, 1)
	go func() {
		_, err := w.handler().Run(context.Background(), s2.SessionID, "e2e-node", s2.FenceToken)
		done2 <- err
	}()
	w.waitState(s2.SessionID, protocol.SessionStreaming, 30*time.Second)
	// Restored gen1 must be live (we wiped the dir before run2).
	got, err := os.ReadFile(filepath.Join(w.liveDir, "slot1.sav"))
	if err != nil || string(got) != "progress-1" {
		t.Fatalf("gen1 not restored: %v %q", err, got)
	}
	if err := os.WriteFile(filepath.Join(w.liveDir, "slot1.sav"), []byte("progress-2"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	w.fake.Stop(backend.Game{Manifest: w.manifest, Host: "e2e-host"})
	select {
	case err := <-done2:
		if err != nil {
			t.Fatalf("run2: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("run2 did not finish")
	}
	s2s := w.getSession(s2.SessionID)
	if s2s.State != protocol.SessionClosed || len(s2s.SaveGens) != 1 || s2s.SaveGens[0] != 2 {
		t.Fatalf("run2 end wrong: %+v", s2s)
	}

	// RUN 3 (JOURNEY E): the old node is destroyed — fresh empty data dirs
	// on a "new node". The game must rebuild from its authorized source,
	// gen2 must restore, and play must save gen3. Persistent history and
	// the latest pointer survive in the control plane.
	base3 := t.TempDir()
	dirs3 := acquire.Dirs{
		Download: filepath.Join(base3, "data", "cache", "downloads"),
		Extract:  filepath.Join(base3, "data", "cache", "extract"),
		Games:    filepath.Join(base3, "data", "games"),
		State:    filepath.Join(base3, "data", "state"),
	}
	live3 := filepath.Join(base3, "profile", "saves")
	m3 := w.manifest
	m3.Saves.Overrides = []protocol.SaveOverride{{Platform: "any", Paths: []string{live3}}}
	if err := w.cat.Put(m3); err != nil {
		t.Fatalf("catalog update: %v", err)
	}
	s3 := w.createSession()
	done3 := make(chan error, 1)
	go func() {
		_, err := w.handlerFor(dirs3, filepath.Join(base3, "data", "staging")).Run(
			context.Background(), s3.SessionID, "e2e-node", s3.FenceToken)
		done3 <- err
	}()
	w.waitState(s3.SessionID, protocol.SessionStreaming, 30*time.Second)
	got3, err := os.ReadFile(filepath.Join(live3, "slot1.sav"))
	if err != nil || string(got3) != "progress-2" {
		t.Fatalf("gen2 not restored on new node: %v %q", err, got3)
	}
	if err := os.WriteFile(filepath.Join(live3, "slot1.sav"), []byte("progress-3"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	w.fake.Stop(backend.Game{Manifest: m3, Host: "e2e-host"})
	select {
	case err := <-done3:
		if err != nil {
			t.Fatalf("run3: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("run3 did not finish")
	}
	s3s := w.getSession(s3.SessionID)
	if s3s.State != protocol.SessionClosed || len(s3s.SaveGens) != 1 || s3s.SaveGens[0] != 3 {
		t.Fatalf("run3 end wrong: %+v", s3s)
	}
	if _, err := os.Stat(filepath.Join(dirs3.Games, "e2e-game", "game.exe")); err != nil {
		t.Fatalf("game rebuilt on new node: %v", err)
	}

	// Playtime/history accumulated server-side across both runs.
	resp, err := http.Get(w.apiURL + "/v1/stats/playtime?user_id=u1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var pt struct {
		Playtime []struct {
			GameID string `json:"game_id"`
			Total  int64  `json:"total_active_seconds"`
			N      int    `json:"sessions"`
		} `json:"playtime"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pt); err != nil {
		t.Fatal(err)
	}
	if len(pt.Playtime) != 1 || pt.Playtime[0].N != 3 {
		t.Fatalf("playtime wrong: %+v", pt)
	}
}
