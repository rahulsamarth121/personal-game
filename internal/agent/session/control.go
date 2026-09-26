// Package session implements the agent-side session handler: the REAL
// end-to-end flow prepare -> restore -> backend start -> READY/STREAMING
// -> final save -> close, driven against the control-plane API. Every
// state reported is observed, never assumed; 409 FENCED stops all work.
package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/personal-game/personal-game/pkg/protocol"
)

// ErrFenced is returned when the control plane reports this node's lease
// is dead. Callers must stop the game/backend immediately and never commit.
var ErrFenced = errors.New("session: fenced by control plane")

// Control is a minimal control-plane client for the session flow.
type Control struct {
	BaseURL string
	HTTP    *http.Client
}

// NewControl builds a client with a sane timeout.
func NewControl(baseURL string) *Control {
	return &Control{BaseURL: baseURL, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Control) post(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPost, path, body, out)
}

func (c *Control) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

func (c *Control) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("session: control %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusConflict {
		return ErrFenced
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("session: control %s %s: status %d: %s", method, path, resp.StatusCode, tail(raw))
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("session: bad control response: %w", err)
		}
	}
	return nil
}

func tail(b []byte) string {
	if len(b) > 500 {
		return string(b[:500])
	}
	return string(b)
}

// NodeSessions lists sessions assigned to this node that need a runner.
// Empty (not error) means idle.
func (c *Control) NodeSessions(ctx context.Context, nodeID string) ([]protocol.Session, error) {
	var out struct {
		Sessions []protocol.Session `json:"sessions"`
	}
	if err := c.get(ctx, "/v1/nodes/"+nodeID+"/sessions", &out); err != nil {
		return nil, err
	}
	if out.Sessions == nil {
		return []protocol.Session{}, nil
	}
	return out.Sessions, nil
}

// GetSession fetches the session (assignment + pinned fence token check).
func (c *Control) GetSession(ctx context.Context, id string) (*protocol.Session, error) {
	var s protocol.Session
	if err := c.get(ctx, "/v1/sessions/"+id, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// GetManifest fetches the game manifest for preparation.
func (c *Control) GetManifest(ctx context.Context, gameID string) (*protocol.GameManifest, error) {
	var m protocol.GameManifest
	if err := c.get(ctx, "/v1/games/"+gameID+"/manifest", &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// Observe reports node-observed state (drives READY/STREAMING/DEGRADED).
func (c *Control) Observe(ctx context.Context, id, nodeID string, token uint64, state protocol.SessionState, gameActive bool) (*protocol.Session, error) {
	var s protocol.Session
	err := c.post(ctx, "/v1/sessions/"+id+"/observe", map[string]any{
		"node_id": nodeID, "fence_token": token,
		"state": state, "game_active": gameActive,
	}, &s)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// SetStream publishes Moonlight connection info after backend readiness.
func (c *Control) SetStream(ctx context.Context, id, nodeID string, token uint64, cfg protocol.StreamConfig) error {
	return c.post(ctx, "/v1/sessions/"+id+"/stream", map[string]any{
		"node_id": nodeID, "fence_token": token,
		"provider": cfg.Provider, "host": cfg.Host, "port": cfg.Port, "app": cfg.App,
	}, nil)
}

// SaveBegin asks the control plane for a generation + upload URL.
func (c *Control) SaveBegin(ctx context.Context, id, nodeID string, token uint64) (gen uint64, uploadURL, key string, err error) {
	var out struct {
		Generation uint64 `json:"generation"`
		UploadURL  string `json:"upload_url"`
		Key        string `json:"key"`
	}
	if err := c.post(ctx, "/v1/sessions/"+id+"/saves/begin",
		map[string]any{"node_id": nodeID, "fence_token": token}, &out); err != nil {
		return 0, "", "", err
	}
	return out.Generation, out.UploadURL, out.Key, nil
}

// SaveCommit finalizes a generation after upload.
func (c *Control) SaveCommit(ctx context.Context, id, nodeID string, token, gen uint64, sha string, size uint64, files int, checkpoint bool) error {
	return c.post(ctx, "/v1/sessions/"+id+"/saves/commit", map[string]any{
		"node_id": nodeID, "fence_token": token, "generation": gen,
		"sha256": sha, "size_bytes": size, "file_count": files, "checkpoint": checkpoint,
	}, nil)
}

// LatestSave returns the latest snapshot + download URL (404-ish error
// when the game was never saved: message contains "no saves yet").
func (c *Control) LatestSave(ctx context.Context, id string) (*protocol.SaveSnapshot, string, error) {
	var out struct {
		Snapshot    protocol.SaveSnapshot `json:"snapshot"`
		DownloadURL string                `json:"download_url"`
	}
	if err := c.get(ctx, "/v1/sessions/"+id+"/saves/latest", &out); err != nil {
		return nil, "", err
	}
	return &out.Snapshot, out.DownloadURL, nil
}

// CloseSession ends the session with a reason.
func (c *Control) CloseSession(ctx context.Context, id, nodeID string, token uint64, reason protocol.SessionEndReason) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		fmt.Sprintf("%s/v1/sessions/%s?node_id=%s&fence_token=%d&reason=%s",
			c.BaseURL, id, nodeID, token, reason), nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode == http.StatusConflict {
		return ErrFenced
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("session: close status %d", resp.StatusCode)
	}
	return nil
}
