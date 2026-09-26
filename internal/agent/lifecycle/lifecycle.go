// Package lifecycle drives the agent-side node state machine: enroll with
// retry, heartbeat/lease renewal loop, and fence-loss detection. The node
// dials out; the control plane never dials in.
package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/personal-game/personal-game/internal/agent/capability"
	"github.com/personal-game/personal-game/internal/common"
	"github.com/personal-game/personal-game/pkg/protocol"
)

// Client talks to the control plane over HTTP (phase 1; same contract will
// ride the WebSocket channel later without changing callers).
type Client struct {
	BaseURL    string
	NodeID     string
	EnrollTok  string
	HTTP       *http.Client
	FenceToken uint64
	LeaseTTL   time.Duration
	State      protocol.NodeState
}

// New builds a client in BOOT state.
func New(baseURL, nodeID, enrollTok string) *Client {
	return &Client{
		BaseURL:   baseURL,
		NodeID:    nodeID,
		EnrollTok: enrollTok,
		HTTP:      &http.Client{Timeout: 15 * time.Second},
		State:     protocol.NodeBoot,
	}
}

// setState enforces the agent-side transition table.
func (c *Client) setState(to protocol.NodeState) error {
	if !c.State.CanTransition(to) {
		return common.E(common.CodeConflict,
			fmt.Sprintf("illegal node transition %s -> %s", c.State, to), nil)
	}
	c.State = to
	return nil
}

// Enroll registers the node, retrying with backoff. On success the node is
// IDLE with a fresh fence token and lease TTL.
func (c *Client) Enroll(ctx context.Context) error {
	if err := c.setState(protocol.NodeProbing); err != nil {
		return err
	}
	caps := capability.Discover()
	if err := c.setState(protocol.NodeRegistering); err != nil {
		return err
	}
	body, _ := json.Marshal(protocol.EnrollRequest{
		NodeID: c.NodeID, EnrollToken: c.EnrollTok, Caps: caps,
	})
	var last error
	for attempt := 0; attempt < 6; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
			c.BaseURL+"/v1/nodes/enroll", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.HTTP.Do(req)
		if err != nil {
			last = err
			time.Sleep(backoff(attempt))
			continue
		}
		var out protocol.EnrollResponse
		derr := json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || derr != nil {
			last = fmt.Errorf("lifecycle: enroll status %d", resp.StatusCode)
			time.Sleep(backoff(attempt))
			continue
		}
		c.FenceToken = out.FenceToken
		c.LeaseTTL = time.Duration(out.LeaseTTLSecs) * time.Second
		if c.LeaseTTL <= 0 {
			c.LeaseTTL = common.DefaultLeaseTTL
		}
		return c.setState(protocol.NodeIdle)
	}
	return common.E(common.CodeStorage, "enroll failed after retries", last)
}

// HeartbeatOnce performs a single lease renewal. It reports whether the
// node is still fenced-in (false = zombie, must stop work immediately).
func (c *Client) HeartbeatOnce(ctx context.Context) (bool, error) {
	body, _ := json.Marshal(protocol.HeartbeatRequest{
		NodeID: c.NodeID, FenceToken: c.FenceToken, State: c.State,
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/v1/nodes/heartbeat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	var out protocol.HeartbeatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, err
	}
	if out.Fenced {
		return false, nil
	}
	c.FenceToken = out.FenceToken
	if out.LeaseTTLSecs > 0 {
		c.LeaseTTL = time.Duration(out.LeaseTTLSecs) * time.Second
	}
	return true, nil
}

// Loop renews the lease until ctx ends or the node is fenced.
// interval should be well below the lease TTL (e.g. TTL/3).
func (c *Client) Loop(ctx context.Context, interval time.Duration, onFenced func()) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			ok, err := c.HeartbeatOnce(ctx)
			if err != nil {
				continue // transient: keep lease until TTL proves otherwise
			}
			if !ok {
				if onFenced != nil {
					onFenced()
				}
				return common.E(common.CodeFenced, "node fenced: stopping work", nil)
			}
		}
	}
}

func backoff(attempt int) time.Duration {
	d := time.Second << attempt // 1s,2s,4s,8s,16s,32s
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}
