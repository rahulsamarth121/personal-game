// Package wolf integrates the node agent with Wolf / Games on Whales.
//
// All calls use Wolf's documented REST API over its Unix socket
// (WOLF_SOCKET_PATH, default /var/run/wolf/wolf.sock or $XDG_RUNTIME_DIR).
// Verified against Wolf's own endpoints.cpp: apps list/add/delete,
// sessions list/stop, pair pending/client, clients, openapi-schema.
// Only structured JSON responses are consumed — never log output.
//
// Access stays LOCAL ONLY: Internet -> control plane -> authenticated node
// channel -> node agent -> Unix socket -> Wolf. This package never listens
// on TCP and never proxies the socket anywhere.
package wolf

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

// DefaultSocketPath is used when WOLF_SOCKET_PATH is unset.
const DefaultSocketPath = "/var/run/wolf/wolf.sock"

// Client talks to Wolf over its Unix socket.
type Client struct {
	SocketPath string
	Timeout    time.Duration
}

// NewClient resolves the socket path (WOLF_SOCKET_PATH wins).
func NewClient() *Client {
	path := os.Getenv("WOLF_SOCKET_PATH")
	if path == "" {
		if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
			path = xdg + "/wolf.sock"
		} else {
			path = DefaultSocketPath
		}
	}
	return &Client{SocketPath: path, Timeout: 15 * time.Second}
}

// Detect reports whether the socket exists. Missing socket is a normal,
// actionable state (Wolf not installed/running), never a silent fake.
func (c *Client) Detect() error {
	st, err := os.Stat(c.SocketPath)
	if err != nil {
		return fmt.Errorf("wolf: socket not found at %s (is Wolf running with WOLF_SOCKET_PATH set?): %w",
			c.SocketPath, err)
	}
	if st.IsDir() {
		return fmt.Errorf("wolf: socket path %s is a directory", c.SocketPath)
	}
	return nil
}

func (c *Client) httpClient() *http.Client {
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", c.SocketPath)
	}
	return &http.Client{
		Transport: &http.Transport{DialContext: dial},
		Timeout:   c.Timeout,
	}
}

// wolfError decodes Wolf's {"error": "..."} shape.
type wolfError struct {
	Error string `json:"error"`
}

// call performs one API call and decodes success:true JSON into out.
// Non-2xx and success:false become descriptive errors carrying Wolf's text.
func (c *Client) call(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://localhost"+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("wolf: socket call %s %s failed: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var we wolfError
		if jerr := json.Unmarshal(raw, &we); jerr == nil && we.Error != "" {
			return fmt.Errorf("wolf: %s %s: %s", method, path, we.Error)
		}
		return fmt.Errorf("wolf: %s %s: http %d", method, path, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("wolf: malformed response from %s %s: %w", method, path, err)
	}
	return nil
}

// App is the subset of Wolf's App shape we depend on.
type App struct {
	Title string `json:"title"`
	ID    string `json:"id"`
}

type appListResponse struct {
	Success bool `json:"success"`
	Apps    []struct {
		Base App `json:"base"`
	} `json:"apps"`
}

// Apps lists Wolf's configured applications (structured API, verified:
// GET /api/v1/apps -> {success, apps:[{base:{title,id,...}}]}).
func (c *Client) Apps(ctx context.Context) ([]App, error) {
	var res appListResponse
	if err := c.call(ctx, http.MethodGet, "/api/v1/apps", nil, &res); err != nil {
		return nil, err
	}
	if !res.Success {
		return nil, fmt.Errorf("wolf: app list reported failure")
	}
	out := make([]App, 0, len(res.Apps))
	for _, a := range res.Apps {
		out = append(out, a.Base)
	}
	return out, nil
}

// HasApp reports whether an application with this exact title is configured.
// Missing apps fail with setup instructions, never silent substitution.
func (c *Client) HasApp(ctx context.Context, title string) error {
	apps, err := c.Apps(ctx)
	if err != nil {
		return err
	}
	for _, a := range apps {
		if a.Title == title {
			return nil
		}
	}
	return fmt.Errorf("wolf: app %q not configured (add it to Wolf config.toml profiles, then retry)", title)
}

// Session is a Wolf stream session matched to one of our apps.
type Session struct {
	ID       string
	AppTitle string
	Raw      map[string]any
}

type sessionListResponse struct {
	Success  bool             `json:"success"`
	Sessions []map[string]any `json:"sessions"`
}

// Sessions lists active Wolf stream sessions (GET /api/v1/sessions).
func (c *Client) Sessions(ctx context.Context) ([]Session, error) {
	var res sessionListResponse
	if err := c.call(ctx, http.MethodGet, "/api/v1/sessions", nil, &res); err != nil {
		return nil, err
	}
	if !res.Success {
		return nil, fmt.Errorf("wolf: session list reported failure")
	}
	out := make([]Session, 0, len(res.Sessions))
	for _, s := range res.Sessions {
		out = append(out, Session{ID: stringField(s, "session_id"), AppTitle: findTitle(s), Raw: s})
	}
	return out, nil
}

// SessionForApp returns the active session streaming the given app title,
// or nil when Moonlight has not started it yet.
func (c *Client) SessionForApp(ctx context.Context, title string) (*Session, error) {
	all, err := c.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	for i, s := range all {
		if s.AppTitle == title && s.ID != "" {
			return &all[i], nil
		}
	}
	return nil, nil
}

// StopSession ends a Wolf session by id (POST /api/v1/sessions/stop).
func (c *Client) StopSession(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("wolf: empty session id")
	}
	var res struct {
		Success bool `json:"success"`
	}
	if err := c.call(ctx, http.MethodPost, "/api/v1/sessions/stop",
		map[string]string{"session_id": sessionID}, &res); err != nil {
		return err
	}
	if !res.Success {
		return fmt.Errorf("wolf: stop reported failure")
	}
	return nil
}

// PairedClient is a Moonlight client Wolf knows.
type PairedClient struct {
	ID string `json:"client_id"`
}

type clientListResponse struct {
	Success bool `json:"success"`
	Clients []struct {
		ID string `json:"client_id"`
	} `json:"clients"`
}

// Paired reports whether any Moonlight client is paired (GET /api/v1/clients).
func (c *Client) Paired(ctx context.Context) (bool, error) {
	var res clientListResponse
	if err := c.call(ctx, http.MethodGet, "/api/v1/clients", nil, &res); err != nil {
		return false, err
	}
	return res.Success && len(res.Clients) > 0, nil
}

// PendingPair lists clients waiting for PIN approval.
func (c *Client) PendingPair(ctx context.Context) ([]map[string]any, error) {
	var res struct {
		Requests []map[string]any `json:"requests"`
	}
	if err := c.call(ctx, http.MethodGet, "/api/v1/pair/pending", nil, &res); err != nil {
		return nil, err
	}
	return res.Requests, nil
}

// PairClient approves a pending client with its PIN
// (POST /api/v1/pair/client {pair_secret, pin}).
func (c *Client) PairClient(ctx context.Context, secret, pin string) error {
	var res struct {
		Success bool `json:"success"`
	}
	if err := c.call(ctx, http.MethodPost, "/api/v1/pair/client",
		map[string]string{"pair_secret": secret, "pin": pin}, &res); err != nil {
		return err
	}
	if !res.Success {
		return fmt.Errorf("wolf: pairing reported failure")
	}
	return nil
}

// stringField reads a string-or-number JSON field (Wolf serializes
// session_id as a string number).
func stringField(m map[string]any, key string) string {
	switch v := m[key].(type) {
	case string:
		return v
	case float64:
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v))
		}
		return fmt.Sprintf("%v", v)
	default:
		return ""
	}
}

// findTitle recursively searches a decoded session object for a "title"
// string (the app it streams). Unknown shapes yield "" rather than a guess.
func findTitle(v any) string {
	switch t := v.(type) {
	case map[string]any:
		if s, ok := t["title"].(string); ok && s != "" {
			return s
		}
		for _, child := range t {
			if s := findTitle(child); s != "" {
				return s
			}
		}
	case []any:
		for _, child := range t {
			if s := findTitle(child); s != "" {
				return s
			}
		}
	}
	return ""
}
