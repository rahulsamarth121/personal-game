package unit

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/personal-game/personal-game/internal/agent/lifecycle"
	"github.com/personal-game/personal-game/internal/agent/stream"
	"github.com/personal-game/personal-game/internal/control/api"
	"github.com/personal-game/personal-game/internal/control/catalog"
	"github.com/personal-game/personal-game/internal/control/nodes"
	"github.com/personal-game/personal-game/pkg/protocol"
)

func TestEnrollHeartbeatFence(t *testing.T) {
	reg := nodes.NewWithLease(50 * time.Millisecond)
	srv := api.New(catalog.New(), reg)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	cl := lifecycle.New(ts.URL, "node-a", "tok")
	cl.HTTP = ts.Client()
	if err := cl.Enroll(context.Background()); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if cl.State != protocol.NodeIdle || cl.FenceToken == 0 {
		t.Fatalf("expected IDLE with token, got %+v", cl)
	}
	ok, err := cl.HeartbeatOnce(context.Background())
	if err != nil || !ok {
		t.Fatalf("heartbeat should renew: ok=%v err=%v", ok, err)
	}
	// Simulate a network partition + replacement: re-enroll bumps the token.
	reg.Enroll("node-a", capableCaps())
	ok, err = cl.HeartbeatOnce(context.Background())
	if err != nil || ok {
		t.Fatalf("zombie heartbeat must report fenced: ok=%v err=%v", ok, err)
	}
	if !reg.Fenced("node-a", cl.FenceToken) {
		t.Fatal("registry must consider the stale token fenced")
	}
	// Unknown nodes are never trusted.
	if !reg.Fenced("ghost", 1) {
		t.Fatal("unknown node must count as fenced")
	}
}

func TestHeartbeatEndpointShapes(t *testing.T) {
	reg := nodes.New()
	srv := api.New(catalog.New(), reg)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	post := func(path string, v any) (int, map[string]any) {
		raw, _ := json.Marshal(v)
		resp, err := http.Post(ts.URL+path, "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	code, body := post("/v1/nodes/enroll", map[string]any{"node_id": "n1", "caps": map[string]any{}})
	if code != 200 || body["fence_token"] == nil || body["lease_ttl_secs"] == nil {
		t.Fatalf("enroll shape wrong: %d %+v", code, body)
	}
	code, body = post("/v1/nodes/heartbeat", map[string]any{"node_id": "n1", "fence_token": body["fence_token"]})
	if code != 200 || body["fenced"] != nil && body["fenced"] == true {
		t.Fatalf("heartbeat should renew: %d %+v", code, body)
	}
	code, body = post("/v1/nodes/heartbeat", map[string]any{"node_id": "n1", "fence_token": 9999})
	if code != http.StatusConflict || body["fenced"] != true {
		t.Fatalf("stale token must 409+fenced: %d %+v", code, body)
	}
}

func TestStreamSelect(t *testing.T) {
	b, err := stream.Select(protocol.Capabilities{Sunshine: true, Wolf: true})
	if err != nil || b.Name() != "sunshine" {
		t.Fatalf("expected sunshine default, got %v %v", b, err)
	}
	b, err = stream.Select(protocol.Capabilities{Wolf: true})
	if err != nil || b.Name() != "wolf" {
		t.Fatalf("expected wolf fallback, got %v %v", b, err)
	}
	if _, err := stream.Select(protocol.Capabilities{}); err == nil {
		t.Fatal("no backend must fail clearly, not silently")
	}
	ep := stream.Sunshine{}.Endpoint("100.64.0.2")
	if ep.Host != "100.64.0.2" || ep.Port == 0 {
		t.Fatalf("bad endpoint: %+v", ep)
	}
}
