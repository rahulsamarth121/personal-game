package main

// Agent entrypoint: discovers capabilities, enrolls with the control plane,
// renews the lease via heartbeat, and picks up assigned sessions
// automatically (PG_SESSION_ID pins one session for developer/debug mode).
// Each session runs prepare -> restore -> backend -> READY/STREAMING ->
// final save -> close. Exits non-zero on invalid config.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/personal-game/personal-game/internal/agent/acquire"
	"github.com/personal-game/personal-game/internal/agent/backend"
	"github.com/personal-game/personal-game/internal/agent/cache"
	"github.com/personal-game/personal-game/internal/agent/capability"
	"github.com/personal-game/personal-game/internal/agent/lifecycle"
	"github.com/personal-game/personal-game/internal/agent/session"
	"github.com/personal-game/personal-game/internal/agent/stream"
	"github.com/personal-game/personal-game/internal/common"
)

func main() {
	cfg, err := common.LoadAgentConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent: invalid config:", err)
		os.Exit(1)
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "agent: invalid config:", err)
		os.Exit(1)
	}
	if cfg.NodeID == "" {
		if h, err := os.Hostname(); err == nil && h != "" {
			cfg.NodeID = h
		} else {
			fmt.Fprintln(os.Stderr, "agent: PG_NODE_ID empty and hostname unavailable")
			os.Exit(1)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	caps := capability.Discover()
	// Media path comes from the capability report (provider-neutral). The
	// node only advertises sessions on it when the endpoint is measured
	// usable AND the operator allowed streaming (STREAMING_ALLOWED).
	host := caps.Network.MediaNetwork.Endpoint
	if media := caps.Network.MediaNetwork; media.Provider != "" {
		if media.Reachable {
			fmt.Fprintf(os.Stderr, "agent: media network %s at %s (%s)\n",
				media.Provider, media.Endpoint, media.Detail)
		} else {
			fmt.Fprintf(os.Stderr, "agent: media network %s unusable: %s\n",
				media.Provider, media.Detail)
		}
	}
	if caps.StreamingAllowed {
		fmt.Fprintln(os.Stderr, "agent: streaming allowed (STREAMING_ALLOWED=true)")
	} else {
		fmt.Fprintln(os.Stderr, "agent: streaming NOT allowed (set STREAMING_ALLOWED=true on a permitted host); sessions will never be assigned")
	}
	legacy, berr := stream.Select(caps)
	if berr != nil {
		fmt.Fprintln(os.Stderr, "agent: warning:", berr, "(continuing: game prep still possible)")
	}

	cl := lifecycle.New(cfg.ControlURL, cfg.NodeID, cfg.EnrollToken)
	if err := cl.Enroll(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "agent: enroll failed:", err)
		os.Exit(1)
	}
	if host == "" {
		host = "<media-endpoint-pending>"
	}
	status := struct {
		NodeID     string `json:"node_id"`
		State      string `json:"state"`
		FenceToken uint64 `json:"fence_token"`
		Backend    string `json:"backend,omitempty"`
		Moonlight  string `json:"moonlight,omitempty"`
	}{
		NodeID: cfg.NodeID, State: string(cl.State), FenceToken: cl.FenceToken,
	}
	if berr == nil {
		ep := legacy.Endpoint(host)
		status.Backend = ep.Backend
		status.Moonlight = fmt.Sprintf("%s:%d", ep.Host, ep.Port)
	}
	out, _ := json.MarshalIndent(status, "", "  ")
	fmt.Println(string(out))

	// Lease renewal always runs; a lost lease fences session work through
	// the handler's control calls (409 -> stop everything, never commit).
	hbCtx, hbStop := context.WithCancel(context.Background())
	defer hbStop()
	go func() {
		interval := cl.LeaseTTL / 3
		if interval <= 0 {
			interval = cfg.HeartbeatInt
		}
		_ = cl.Loop(hbCtx, interval, func() {
			fmt.Fprintln(os.Stderr, "agent: fenced — lease lost")
		})
	}()

	if sessionID := os.Getenv("PG_SESSION_ID"); sessionID != "" {
		// Developer/debug mode: run exactly one session.
		os.Exit(runSession(ctx, cfg, cl, sessionID, host))
	}
	runWorkLoop(ctx, cfg, cl, host)
}

// runWorkLoop polls for assigned sessions and runs them one at a time.
// No manual session IDs needed in normal use.
func runWorkLoop(ctx context.Context, cfg common.AgentConfig, cl *lifecycle.Client, host string) {
	ctl := session.NewControl(cfg.ControlURL)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			work, err := ctl.NodeSessions(ctx, cfg.NodeID)
			if err != nil {
				fmt.Fprintln(os.Stderr, "agent: pickup failed:", err)
				continue
			}
			for _, s := range work {
				if s.FenceToken != cl.FenceToken {
					fmt.Fprintln(os.Stderr, "agent: skipping session with stale token:", s.SessionID)
					continue
				}
				fmt.Fprintln(os.Stderr, "agent: picked up session", s.SessionID, "game", s.GameID)
				code := runSession(ctx, cfg, cl, s.SessionID, host)
				fmt.Fprintln(os.Stderr, "agent: session", s.SessionID, "finished (exit", code, ")")
			}
		}
	}
}

// runSession executes one assigned session through the gaming backend.
func runSession(ctx context.Context, cfg common.AgentConfig, cl *lifecycle.Client, sessionID, host string) int {
	be, avail := backend.Select()
	if avail.Mode != backend.ModeReal {
		fmt.Fprintln(os.Stderr, "agent: cannot run session:", avail.Reason)
		return 2
	}
	fmt.Fprintln(os.Stderr, "agent: backend", be.Name(), "REAL")
	dirs := acquire.Dirs{
		Download: cfg.CacheDir + "/downloads",
		Extract:  cfg.CacheDir + "/extract",
		Games:    cfg.GamesDir,
		State:    cfg.StateDir,
	}
	// Warm-cache registry (best effort: a corrupt index rebuilds from scans,
	// a missing one starts empty — installed files stay authoritative).
	var cacher *cache.Registry
	var evictable func(string) uint64
	if reg, err := cache.Open(cfg.StateDir + "/cache.json"); err != nil {
		fmt.Fprintln(os.Stderr, "agent: cache index unreadable, starting empty:", err)
	} else {
		cacher = reg
		evictable = reg.Evictable
	}
	h := &session.Handler{
		Control:        session.NewControl(cfg.ControlURL),
		Backend:        be,
		Dirs:           dirs,
		SaveStaging:    cfg.TempRoot + "/saves-staging",
		Host:           host,
		DiskFree:       capability.FreeSpace,
		EvictableBytes: evictable,
		Cache:          cacher,
		GPUIndex:       cfg.GPUIndex,
		PollInterval:   5 * time.Second,
	}
	if cfg.GPUIndex >= 0 {
		fmt.Fprintln(os.Stderr, "agent: GPU pinned to index", cfg.GPUIndex)
	}
	res, err := h.Run(ctx, sessionID, cfg.NodeID, cl.FenceToken)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent: session failed:", err)
		return 1
	}
	fmt.Fprintln(os.Stderr, "agent: session ended:", res.EndReason,
		"moonlight:", res.Conn.MoonlightArgs)
	return 0
}
