package session

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/personal-game/personal-game/internal/agent/acquire"
	"github.com/personal-game/personal-game/internal/agent/backend"
	"github.com/personal-game/personal-game/internal/agent/cache"
	agentSaves "github.com/personal-game/personal-game/internal/agent/saves"
	"github.com/personal-game/personal-game/internal/common"
	"github.com/personal-game/personal-game/pkg/protocol"
)

// Handler runs one assigned session end to end on the node:
// prepare (warm-cache aware) -> restore save -> backend start ->
// READY/STREAMING observation -> final save -> close. Every reported state
// is observed from the backend/process; nothing is assumed ready.
type Handler struct {
	Control *Control
	Backend backend.GamingBackend
	Dirs    acquire.Dirs

	// SaveStaging is the temp root for save blobs (ephemeral tier).
	SaveStaging string
	// Host is the Moonlight-visible node address (e.g. Tailscale IP).
	Host string
	// DiskFree reports free bytes for a path (capability.FreeSpace).
	DiskFree func(path string) (uint64, error)
	// EvictableBytes reports reclaimable game-cache bytes (0 = none).
	EvictableBytes func(activeGameID string) uint64
	// Cache records installed games for warm-cache reuse and LRU eviction.
	// Nil disables recording (eviction then sees nothing reclaimable).
	Cache *cache.Registry
	// GPUIndex pins the game's NVIDIA device (-1 = unset). Wired from
	// PG_GPU_INDEX by the agent entrypoint.
	GPUIndex int

	PollInterval    time.Duration
	ReadyTimeout    time.Duration
	DisconnectGrace time.Duration
}

func (h *Handler) poll() time.Duration {
	if h.PollInterval > 0 {
		return h.PollInterval
	}
	return 5 * time.Second
}

func (h *Handler) readyTimeout() time.Duration {
	if h.ReadyTimeout > 0 {
		return h.ReadyTimeout
	}
	return 5 * time.Minute
}

func (h *Handler) grace() time.Duration {
	if h.DisconnectGrace > 0 {
		return h.DisconnectGrace
	}
	return 90 * time.Second
}

// Result summarizes a finished session run.
type Result struct {
	EndReason protocol.SessionEndReason
	Conn      backend.ConnInfo
}

// Run executes the session. It returns ErrFenced when the lease died
// (game/backend already stopped); any other error means the run itself
// failed after best-effort cleanup and session close.
func (h *Handler) Run(ctx context.Context, sessionID, nodeID string, token uint64) (Result, error) {
	var res Result
	fail := func(game backend.Game, reason protocol.SessionEndReason, err error) (Result, error) {
		_ = h.Backend.Stop(game)
		_ = h.Control.CloseSession(ctx, sessionID, nodeID, token, reason)
		return res, err
	}

	sess, err := h.Control.GetSession(ctx, sessionID)
	if err != nil {
		return res, err
	}
	if sess.NodeID != nodeID || token != sess.FenceToken {
		return res, ErrFenced
	}
	manifest, err := h.Control.GetManifest(ctx, sess.GameID)
	if err != nil {
		return res, err
	}
	if _, err := h.Control.Observe(ctx, sessionID, nodeID, token, protocol.SessionPreparing, false); err != nil {
		return res, err
	}

	exe, gameRoot, err := h.ensureGame(ctx, *manifest)
	if err != nil {
		_ = h.Control.CloseSession(ctx, sessionID, nodeID, token, protocol.EndAborted)
		return res, fmt.Errorf("session: game preparation failed: %w", err)
	}
	game := backend.Game{Manifest: *manifest, ExePath: exe, WorkDir: gameRoot, Host: h.Host, GPUIndex: h.GPUIndex}

	if err := h.restoreSave(ctx, sessionID, nodeID, token, *manifest); err != nil {
		_ = h.Control.CloseSession(ctx, sessionID, nodeID, token, protocol.EndAborted)
		return res, fmt.Errorf("session: save restore failed: %w", err)
	}

	if err := h.Backend.Start(game); err != nil {
		_ = h.Control.CloseSession(ctx, sessionID, nodeID, token, protocol.EndAborted)
		return res, fmt.Errorf("session: backend start failed: %w", err)
	}
	conn, err := h.Backend.Connection(game)
	if err != nil {
		return fail(game, protocol.EndAborted, err)
	}
	res.Conn = conn
	if err := h.Control.SetStream(ctx, sessionID, nodeID, token, protocol.StreamConfig{
		Provider: conn.Backend, Host: conn.Host, App: conn.App,
	}); err != nil {
		return fail(game, protocol.EndAborted, err)
	}
	if _, err := h.Control.Observe(ctx, sessionID, nodeID, token, protocol.SessionReady, false); err != nil {
		return fail(game, protocol.EndAborted, err)
	}

	// READY means Moonlight can connect now. STREAMING is reported only
	// when the backend actually runs (Wolf session live / process alive).
	if err := h.waitRunning(ctx, sessionID, nodeID, token, game); err != nil {
		return fail(game, protocol.EndAborted, err)
	}

	reason := h.monitor(ctx, sessionID, nodeID, token, game)
	// Final save: best effort on crash, required-ish on clean exit; the
	// session always closes so history/playtime land.
	serr := h.finalSave(ctx, sessionID, nodeID, token, *manifest, reason)
	_ = h.Backend.Stop(game)
	h.cacheRelease(sess.GameID)
	_ = h.Control.CloseSession(ctx, sessionID, nodeID, token, reason)
	res.EndReason = reason
	if serr != nil && (reason == protocol.EndGameExit) {
		return res, fmt.Errorf("session: final save failed: %w", serr)
	}
	return res, nil
}

// resolveExe validates the launch target for an already-prepared game.
func resolveExe(dirs acquire.Dirs, m protocol.GameManifest) (exe, root string, err error) {
	pt := acquire.EffectiveType(m)
	if pt.IsPrebuilt() {
		root, err = acquire.GameRoot(dirs, m)
		if err != nil {
			return "", "", err
		}
		if err := acquire.ValidatePrebuilt(root, m); err != nil {
			return "", "", err
		}
		exe, err = common.ValidatePath([]string{root}, filepath.Join(root, m.Launch.Executable))
		return exe, root, err
	}
	root = filepath.Join(dirs.Games, m.GameID)
	exe, err = acquire.ValidateLaunchTarget(dirs.Games, m.GameID, m.Launch)
	if err != nil {
		return "", "", err
	}
	return exe, root, nil
}

// ensureGame uses the warm cache when valid, otherwise runs acquisition,
// then records the result in the cache registry.
func (h *Handler) ensureGame(ctx context.Context, m protocol.GameManifest) (string, string, error) {
	if exe, root, err := resolveExe(h.Dirs, m); err == nil {
		h.cacheTouch(m.GameID)
		return exe, root, nil
	}
	free, ferr := h.freeBytes()
	if ferr != nil {
		return "", "", fmt.Errorf("disk free unreadable: %w", ferr)
	}
	var evictable uint64
	if h.EvictableBytes != nil {
		evictable = h.EvictableBytes(m.GameID)
	}
	// Reclaim LRU game cache before downloading when eviction covers the
	// shortfall; otherwise Prepare's gate aborts cleanly with the numbers.
	if h.Cache != nil {
		if rep := acquire.CheckDiskFor(m, free, evictable); !rep.Fits && rep.FitsAfterEvict {
			if freed, ok := cache.EvictForNeed(h.Dirs.Games, h.Cache, m.GameID, rep.RequiredBytes-free); ok {
				free += freed
			}
		}
	}
	if _, err := acquire.Prepare(ctx, h.Dirs, m, &acquire.Downloader{UseAria2c: true}, free, evictable); err != nil {
		return "", "", err
	}
	exe, root, err := resolveExe(h.Dirs, m)
	if err != nil {
		return "", "", err
	}
	if h.Cache != nil {
		_ = h.Cache.MarkInstalled(m.GameID, m.Version, m.Footprint.InstalledBytes)
		_ = h.Cache.Protect(m.GameID, true)
	}
	return exe, root, nil
}

// cacheRelease unprotects the game after the run (best effort).
func (h *Handler) cacheRelease(gameID string) {
	if h.Cache != nil {
		_ = h.Cache.Touch(gameID)
		_ = h.Cache.Protect(gameID, false)
	}
}

func (h *Handler) cacheTouch(gameID string) {
	if h.Cache != nil {
		_ = h.Cache.Touch(gameID)
		_ = h.Cache.Protect(gameID, true)
	}
}

func (h *Handler) freeBytes() (uint64, error) {
	if h.DiskFree != nil {
		return h.DiskFree(h.Dirs.Download)
	}
	return 0, fmt.Errorf("no disk probe configured")
}

// restoreSave fetches the latest persistent save (skips honestly when the
// game was never saved) and swaps it into the live save locations.
func (h *Handler) restoreSave(ctx context.Context, sessionID, nodeID string, token uint64, m protocol.GameManifest) error {
	snap, dlURL, err := h.Control.LatestSave(ctx, sessionID)
	if err != nil {
		if strings.Contains(err.Error(), "no saves yet") || strings.Contains(err.Error(), "404") {
			return nil // first play: nothing to restore
		}
		return err
	}
	blob, err := downloadBytes(dlURL)
	if err != nil {
		return err
	}
	stage := filepath.Join(h.SaveStaging, sessionID)
	os.RemoveAll(stage)
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	tmp := filepath.Join(stage, "blob.zip")
	if err := os.WriteFile(tmp, blob, 0o644); err != nil {
		return err
	}
	mode, _ := agentSaves.ModeFor(m.Saves.LudusaviTitle, len(m.Saves.Overrides) > 0)
	switch mode {
	case agentSaves.SaveModeLudusavi:
		return agentSaves.RestoreLudusavi(stage, tmp, snap.SHA256, m.Saves.LudusaviTitle)
	default:
		live, err := agentSaves.ResolveDirs(m)
		if err != nil {
			return err
		}
		return agentSaves.RestoreFromBlob(stage, live, tmp, snap.SHA256)
	}
}

// waitRunning polls until the backend runs (Moonlight connected / process
// alive) or the timeout expires. READY was already reported; STREAMING is
// earned here.
func (h *Handler) waitRunning(ctx context.Context, sessionID, nodeID string, token uint64, game backend.Game) error {
	deadline := time.Now().Add(h.readyTimeout())
	tick := time.NewTicker(h.poll())
	defer tick.Stop()
	for {
		st, err := h.Backend.Status(game)
		if err == nil && st.Running {
			_, oerr := h.Control.Observe(ctx, sessionID, nodeID, token, protocol.SessionStreaming, true)
			return oerr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			if time.Now().After(deadline) {
				return fmt.Errorf("backend never reached running state within %s", h.readyTimeout())
			}
		}
	}
}

// monitor watches the run: heartbeats with gameActive, grace on
// disappearance (client may reconnect), then exit classification.
func (h *Handler) monitor(ctx context.Context, sessionID, nodeID string, token uint64, game backend.Game) protocol.SessionEndReason {
	tick := time.NewTicker(h.poll())
	defer tick.Stop()
	degraded := false
	for {
		select {
		case <-ctx.Done():
			return protocol.EndAborted
		case <-tick.C:
			st, err := h.Backend.Status(game)
			if err != nil {
				if !degraded {
					_, _ = h.Control.Observe(ctx, sessionID, nodeID, token, protocol.SessionDegraded, true)
					degraded = true
				}
				continue
			}
			if st.Running {
				if degraded {
					_, _ = h.Control.Observe(ctx, sessionID, nodeID, token, protocol.SessionStreaming, true)
					degraded = false
				} else {
					_, _ = h.Control.Observe(ctx, sessionID, nodeID, token, "", true)
				}
				continue
			}
			// Not running: grace period for reconnects, then classify.
			if h.graceWait(ctx, game) {
				_, _ = h.Control.Observe(ctx, sessionID, nodeID, token, protocol.SessionStreaming, true)
				continue
			}
			return classifyExit(st)
		}
	}
}

// graceWait returns true when the backend came back within the grace window.
func (h *Handler) graceWait(ctx context.Context, game backend.Game) bool {
	deadline := time.Now().Add(h.grace())
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(h.poll()):
			st, err := h.Backend.Status(game)
			if err == nil && st.Running {
				return true
			}
		}
	}
	return false
}

// classifyExit maps backend truth to end reasons: known-clean process exit
// vs crash vs vanished stream (client gone).
func classifyExit(st backend.Status) protocol.SessionEndReason {
	if st.ExitCode == 0 {
		return protocol.EndGameExit
	}
	if st.BackendSessID != "" && st.ExitCode == -1 {
		// Stream session vanished without a process record (Wolf container
		// stops on Moonlight disconnect by default).
		return protocol.EndClientGone
	}
	return protocol.EndGameCrash
}

// finalSave snapshots live saves and commits a generation. Crash saves are
// best-effort (never fail the close); misconfigured saves skip honestly.
func (h *Handler) finalSave(ctx context.Context, sessionID, nodeID string, token uint64, m protocol.GameManifest, reason protocol.SessionEndReason) error {
	mode, why := agentSaves.ModeFor(m.Saves.LudusaviTitle, len(m.Saves.Overrides) > 0)
	if mode == agentSaves.SaveModeNone {
		return fmt.Errorf("saves skipped: %s", why)
	}
	gen, uploadURL, _, err := h.Control.SaveBegin(ctx, sessionID, nodeID, token)
	if err != nil {
		return err
	}
	stage := filepath.Join(h.SaveStaging, sessionID)
	os.RemoveAll(stage)
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	zipPath := filepath.Join(stage, "save.zip")
	var sha string
	var size uint64
	var files int
	if mode == agentSaves.SaveModeLudusavi {
		sha, size, files, err = agentSaves.SnapshotLudusavi(ctx, m.Saves.LudusaviTitle, stage, zipPath)
		if err != nil {
			return err
		}
		if err := agentSaves.UploadPUT(uploadURL, zipPath, sha); err != nil {
			return err
		}
	} else {
		live, err := agentSaves.ResolveDirs(m)
		if err != nil {
			return err
		}
		sha, size, files, err = agentSaves.Snapshot(live, zipPath, uploadURL, 2*time.Second, 60*time.Second)
		if err != nil {
			return err
		}
	}
	checkpoint := reason != protocol.EndGameExit
	return h.Control.SaveCommit(ctx, sessionID, nodeID, token, gen, sha, size, files, checkpoint)
}

func downloadBytes(dlURL string) ([]byte, error) {
	if dlURL == "" || strings.HasPrefix(dlURL, "mem://") {
		return nil, fmt.Errorf("session: unsupported download scheme (production uses https presigned URLs)")
	}
	resp, err := http.Get(dlURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("session: save download status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<31))
}
