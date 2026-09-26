package unit

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/personal-game/personal-game/internal/agent/backend"
	agentWolf "github.com/personal-game/personal-game/internal/agent/wolf"
)

// fakeWolf serves scripted Wolf API responses over a real Unix socket, so
// the client (socket dial, HTTP, JSON parsing, error surfacing) is tested
// for real. Only the server side is fake, and it says so.
func fakeWolf(t *testing.T, routes map[string]struct {
	code int
	body string
}) *agentWolf.Client {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "wolf.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	mux := http.NewServeMux()
	for path, r := range routes {
		r := r
		mux.HandleFunc(path, func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(r.code)
			_, _ = w.Write([]byte(r.body))
		})
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln) //nolint:errcheck
	t.Cleanup(func() { srv.Close() })
	t.Setenv("WOLF_SOCKET_PATH", sock)
	c := agentWolf.NewClient()
	c.Timeout = 5 * time.Second
	return c
}

func TestWolfApps(t *testing.T) {
	c := fakeWolf(t, map[string]struct {
		code int
		body string
	}{
		"/api/v1/apps": {200, `{"success":true,"apps":[{"base":{"title":"Doom","id":"a1"}},{"base":{"title":"Desktop","id":"a2"}}]}`},
	})
	ctx := context.Background()
	apps, err := c.Apps(ctx)
	if err != nil {
		t.Fatalf("apps: %v", err)
	}
	if len(apps) != 2 || apps[0].Title != "Doom" || apps[0].ID != "a1" {
		t.Fatalf("bad apps: %+v", apps)
	}
	if err := c.HasApp(ctx, "Doom"); err != nil {
		t.Fatalf("hasapp: %v", err)
	}
	if err := c.HasApp(ctx, "Missing"); err == nil {
		t.Fatal("missing app must fail with setup guidance")
	}
}

func TestWolfSessions(t *testing.T) {
	c := fakeWolf(t, map[string]struct {
		code int
		body string
	}{
		"/api/v1/sessions":      {200, `{"success":true,"sessions":[{"session_id":"7","app":{"title":"Doom"}},{"session_id":9,"app":{"title":"Desktop"}}]}`},
		"/api/v1/sessions/stop": {200, `{"success":true}`},
	})
	ctx := context.Background()
	s, err := c.SessionForApp(ctx, "Doom")
	if err != nil || s == nil || s.ID != "7" {
		t.Fatalf("session for app: %+v %v", s, err)
	}
	if s, err := c.SessionForApp(ctx, "Nobody"); err != nil || s != nil {
		t.Fatalf("absent app should be (nil, nil): %+v %v", s, err)
	}
	if err := c.StopSession(ctx, "7"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := c.StopSession(ctx, ""); err == nil {
		t.Fatal("empty session id must fail")
	}
}

func TestWolfFailures(t *testing.T) {
	// Wolf's own error shape must surface, not vanish.
	c := fakeWolf(t, map[string]struct {
		code int
		body string
	}{
		"/api/v1/sessions":      {200, `{"success":true,"sessions":[]}`},
		"/api/v1/sessions/stop": {500, `{"error":"Invalid session_id"}`},
		"/api/v1/apps":          {500, `{"error":"boom"}`},
	})
	ctx := context.Background()
	if err := c.StopSession(ctx, "nope"); err == nil {
		t.Fatal("wolf 500 must surface as error")
	} else if got := err.Error(); got != "wolf: POST /api/v1/sessions/stop: Invalid session_id" {
		t.Fatalf("error should carry wolf text, got %q", got)
	}
	if _, err := c.Apps(ctx); err == nil {
		t.Fatal("apps 500 must surface")
	}
	// Malformed JSON is an error, never a silent empty list.
	c2 := fakeWolf(t, map[string]struct {
		code int
		body string
	}{"/api/v1/apps": {200, `not json`}})
	if _, err := c2.Apps(ctx); err == nil {
		t.Fatal("malformed response must fail")
	}
	// Missing socket is an actionable error.
	t.Setenv("WOLF_SOCKET_PATH", filepath.Join(t.TempDir(), "absent.sock"))
	if err := agentWolf.NewClient().Detect(); err == nil {
		t.Fatal("missing socket must fail detection")
	}
}

func TestWolfPairing(t *testing.T) {
	c := fakeWolf(t, map[string]struct {
		code int
		body string
	}{
		"/api/v1/clients":      {200, `{"success":true,"clients":[{"client_id":"c1"}]}`},
		"/api/v1/pair/pending": {200, `{"requests":[{"pair_secret":"s3","client_ip":"1.2.3.4"}]}`},
		"/api/v1/pair/client":  {200, `{"success":true}`},
	})
	ctx := context.Background()
	paired, err := c.Paired(ctx)
	if err != nil || !paired {
		t.Fatalf("paired: %v %v", paired, err)
	}
	pending, err := c.PendingPair(ctx)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending: %+v %v", pending, err)
	}
	if err := c.PairClient(ctx, "s3", "1234"); err != nil {
		t.Fatalf("pair: %v", err)
	}
}

func TestWolfBackendModes(t *testing.T) {
	fake := fakeWolf(t, map[string]struct {
		code int
		body string
	}{
		"/api/v1/apps":     {200, `{"success":true,"apps":[{"base":{"title":"Doom","id":"a1"}}]}`},
		"/api/v1/clients":  {200, `{"success":true,"clients":[{"client_id":"c1"}]}`},
		"/api/v1/sessions": {200, `{"success":true,"sessions":[]}`},
	})
	_ = fake
	be := backend.NewWolfBackend()
	// Point at the fake socket explicitly (same process, real socket I/O).
	be.Wolf = fakeWolf(t, map[string]struct {
		code int
		body string
	}{
		"/api/v1/apps":          {200, `{"success":true,"apps":[{"base":{"title":"Doom","id":"a1"}}]}`},
		"/api/v1/clients":       {200, `{"success":true,"clients":[{"client_id":"c1"}]}`},
		"/api/v1/sessions":      {200, `{"success":true,"sessions":[{"session_id":"3","app":{"title":"Doom"}}]}`},
		"/api/v1/sessions/stop": {200, `{"success":true}`},
	})
	if avail := be.Probe(); avail.Mode != backend.ModeReal {
		t.Fatalf("probe should be REAL against live socket: %+v", avail)
	}
	game := backend.Game{Host: "100.64.0.2"}
	game.Manifest.GameID = "doom"
	game.Manifest.Name = "Doom"
	game.Manifest.Launch.WolfApp = "Doom"
	if err := be.Start(game); err != nil {
		t.Fatalf("start: %v", err)
	}
	st, err := be.Status(game)
	if err != nil || !st.Running || st.BackendSessID != "3" {
		t.Fatalf("status: %+v %v", st, err)
	}
	conn, err := be.Connection(game)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"stream", "100.64.0.2", "Doom"}
	if len(conn.MoonlightArgs) != 3 || conn.MoonlightArgs[0] != want[0] || conn.MoonlightArgs[2] != want[2] {
		t.Fatalf("moonlight args wrong: %+v", conn.MoonlightArgs)
	}
	if err := be.Stop(game); err != nil {
		t.Fatalf("stop: %v", err)
	}
	// Missing app: actionable failure, never fake-ready.
	game.Manifest.Launch.WolfApp = "Absent"
	if err := be.Start(game); err == nil {
		t.Fatal("missing wolf app must fail")
	}
	// Unreachable socket: UNAVAILABLE with reason.
	t.Setenv("WOLF_SOCKET_PATH", filepath.Join(t.TempDir(), "nope.sock"))
	be2 := backend.NewWolfBackend()
	os.Unsetenv("XDG_RUNTIME_DIR")
	avail := be2.Probe()
	if avail.Mode != backend.ModeUnavailable || avail.Reason == "" {
		// XDG_RUNTIME_DIR may redirect the default; accept either as long as
		// the mode is honest (no fake REAL without a socket).
		if avail.Mode == backend.ModeReal {
			t.Fatal("probe must not claim REAL without a reachable socket")
		}
	}
}

func TestFakeBackendIsMock(t *testing.T) {
	fb := backend.NewFakeBackend()
	if avail := fb.Probe(); avail.Mode != backend.ModeMock {
		t.Fatalf("fake must advertise MOCK: %+v", avail)
	}
	game := backend.Game{Host: "h"}
	game.Manifest.GameID = "g"
	if err := fb.Start(game); err != nil {
		t.Fatal(err)
	}
	st, _ := fb.Status(game)
	if !st.Running {
		t.Fatal("fake should report running after start")
	}
	if err := fb.Stop(game); err != nil {
		t.Fatal(err)
	}
	st, _ = fb.Status(game)
	if st.Running {
		t.Fatal("fake should report stopped after stop")
	}
	// Stop is idempotent.
	if err := fb.Stop(game); err != nil {
		t.Fatal(err)
	}
}

func TestSunshineBackendHonest(t *testing.T) {
	sb := backend.NewSunshineBackend()
	avail := sb.Probe()
	if avail.Mode != backend.ModeReal && avail.Mode != backend.ModeUnavailable {
		t.Fatalf("probe must be REAL or UNAVAILABLE, never MOCK: %+v", avail)
	}
	if avail.Mode == backend.ModeUnavailable && avail.Reason == "" {
		t.Fatal("UNAVAILABLE needs an actionable reason")
	}
	game := backend.Game{}
	game.Manifest.GameID = "g"
	if err := sb.Start(game); err == nil {
		t.Fatal("start without executable must fail")
	}
	// Stop with nothing running is success (idempotent).
	if err := sb.Stop(game); err != nil {
		t.Fatal(err)
	}
	st, err := sb.Status(game)
	if err != nil || st.Running {
		t.Fatalf("absent game must be not-running: %+v %v", st, err)
	}
	conn, err := sb.Connection(backend.Game{Host: "h"})
	if err != nil || conn.App != "Desktop" {
		t.Fatalf("sunshine streams the desktop session: %+v %v", conn, err)
	}
}
