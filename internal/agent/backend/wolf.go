package backend

import (
	"context"
	"fmt"
	"time"

	agentWolf "github.com/personal-game/personal-game/internal/agent/wolf"
)

// WolfBackend drives Wolf through its local Unix-socket API. Streaming
// itself starts when Moonlight launches the app (Wolf spins the container
// on demand); Start verifies everything Moonlight needs is in place, and
// Status observes real Wolf sessions. Never fakes contact with the socket.
type WolfBackend struct {
	Wolf    *agentWolf.Client
	Timeout time.Duration
}

// NewWolfBackend builds a REAL backend against the local Wolf socket.
func NewWolfBackend() *WolfBackend {
	return &WolfBackend{Wolf: agentWolf.NewClient(), Timeout: 20 * time.Second}
}

// Name implements GamingBackend.
func (b *WolfBackend) Name() string { return "wolf" }

// Probe implements GamingBackend: socket + app list must answer.
func (b *WolfBackend) Probe() Availability {
	if err := b.Wolf.Detect(); err != nil {
		return Availability{Mode: ModeUnavailable, Reason: err.Error()}
	}
	ctx, cancel := context.WithTimeout(context.Background(), b.Timeout)
	defer cancel()
	if _, err := b.Wolf.Apps(ctx); err != nil {
		return Availability{Mode: ModeUnavailable, Reason: "socket present but API failed: " + err.Error()}
	}
	return Availability{Mode: ModeReal}
}

// Start implements GamingBackend: the app must exist and a client must be
// paired; otherwise Moonlight could never connect, so fail loudly with the
// exact remediation instead of reporting ready.
func (b *WolfBackend) Start(g Game) error {
	ctx, cancel := context.WithTimeout(context.Background(), b.Timeout)
	defer cancel()
	title := AppTitle(g.Manifest)
	if g.Manifest.Launch.WolfApp == "" {
		// Without an explicit mapping we still try the game name, but say so.
		if err := b.Wolf.HasApp(ctx, title); err != nil {
			return fmt.Errorf("%w (set launch.wolf_app to the Wolf config.toml app title)", err)
		}
		return nil
	}
	if err := b.Wolf.HasApp(ctx, title); err != nil {
		return err
	}
	paired, err := b.Wolf.Paired(ctx)
	if err != nil {
		return err
	}
	if !paired {
		pending, perr := b.Wolf.PendingPair(ctx)
		if perr != nil {
			return perr
		}
		if len(pending) > 0 {
			return fmt.Errorf("wolf: %d client(s) awaiting PIN approval (approve via POST /api/v1/pair/client over the socket, then retry)", len(pending))
		}
		return fmt.Errorf("wolf: no Moonlight client paired (in Moonlight: add %s, enter the PIN shown on the Wolf host)", g.Host)
	}
	return nil
}

// Status implements GamingBackend: a running Wolf session for the app.
func (b *WolfBackend) Status(g Game) (Status, error) {
	ctx, cancel := context.WithTimeout(context.Background(), b.Timeout)
	defer cancel()
	sess, err := b.Wolf.SessionForApp(ctx, AppTitle(g.Manifest))
	if err != nil {
		return Status{}, err
	}
	if sess == nil {
		return Status{Running: false, Detail: "no wolf session for app", ExitCode: -1}, nil
	}
	return Status{Running: true, Detail: "wolf session active", BackendSessID: sess.ID, ExitCode: -1}, nil
}

// Stop implements GamingBackend: ends Wolf sessions for the app (no-op
// when Moonlight never connected).
func (b *WolfBackend) Stop(g Game) error {
	ctx, cancel := context.WithTimeout(context.Background(), b.Timeout)
	defer cancel()
	sess, err := b.Wolf.SessionForApp(ctx, AppTitle(g.Manifest))
	if err != nil {
		return err
	}
	if sess == nil {
		return nil
	}
	return b.Wolf.StopSession(ctx, sess.ID)
}

// Connection implements GamingBackend.
func (b *WolfBackend) Connection(g Game) (ConnInfo, error) {
	app := AppTitle(g.Manifest)
	return ConnInfo{Backend: "wolf", Host: g.Host, App: app, MoonlightArgs: MoonlightArgs(g.Host, app)}, nil
}
