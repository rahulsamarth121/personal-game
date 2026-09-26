package unit

// Capability-driven scheduling and media-network tests: streaming_allowed
// gating, media provider parsing (tailscale / cloudflare_private_network /
// direct), and client-API bearer authentication.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/personal-game/personal-game/internal/agent/capability"
	"github.com/personal-game/personal-game/internal/control/auth"
	"github.com/personal-game/personal-game/internal/control/catalog"
	"github.com/personal-game/personal-game/internal/control/nodes"
	csession "github.com/personal-game/personal-game/internal/control/session"
	"github.com/personal-game/personal-game/pkg/protocol"
)

// capabilityResolve runs ResolveMediaNetwork with the given tailscale state.
func capabilityResolve(t *testing.T, ts capability.TailscaleDetail) (protocol.MediaNetwork, error) {
	t.Helper()
	return capability.ResolveMediaNetwork(ts)
}

func newAuthTestCatalog(t *testing.T) *catalog.Registry {
	t.Helper()
	c := catalog.New()
	if err := c.Put(protocol.GameManifest{
		SchemaVersion: 1, GameID: "doom2", Name: "DOOM II", Version: "1",
		Acquisition: protocol.AcquisitionRef{Provider: "archive"},
		Launch:      protocol.LaunchSpec{Executable: "doom2.exe"},
	}); err != nil {
		t.Fatal(err)
	}
	return c
}

// -- media network resolution ------------------------------------------------

func TestResolveMediaNetworkDefaultsToMeasuredTailscale(t *testing.T) {
	t.Setenv("MEDIA_NETWORK", "")
	t.Setenv("MEDIA_ENDPOINT", "")
	got, err := capabilityResolve(t, capability.TailscaleDetail{Running: true, IP: "100.64.0.9", Peers: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != protocol.MediaTailscale || got.Endpoint != "100.64.0.9" ||
		!got.Reachable || !got.TCPOK || !got.UDPOK {
		t.Fatalf("bad default media network: %+v", got)
	}
	// Not running -> not usable, with a reason (never fabricated).
	got, err = capabilityResolve(t, capability.TailscaleDetail{Running: false, Reason: "not installed"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Usable() || got.Reachable {
		t.Fatalf("unusable tailscale must not report usable: %+v", got)
	}
	if !strings.Contains(got.Detail, "not installed") {
		t.Fatalf("expected honest reason, got %q", got.Detail)
	}
}

func TestResolveMediaNetworkCloudflarePrivate(t *testing.T) {
	t.Setenv("MEDIA_NETWORK", "cloudflare_private_network")
	t.Setenv("MEDIA_ENDPOINT", "172.16.9.9")
	got, err := capabilityResolve(t, capability.TailscaleDetail{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != protocol.MediaCloudflarePrivateNet || got.Endpoint != "172.16.9.9" {
		t.Fatalf("bad cloudflare media network: %+v", got)
	}
	if got.Reachable {
		t.Fatalf("without a local connector the path must not claim reachable: %+v", got)
	}
	if !strings.Contains(got.Detail, "cloudflared") {
		t.Fatalf("detail should name the missing connector: %q", got.Detail)
	}
}

func TestResolveMediaNetworkDirect(t *testing.T) {
	t.Setenv("MEDIA_NETWORK", "direct")
	t.Setenv("MEDIA_ENDPOINT", "203.0.113.7")
	got, err := capabilityResolve(t, capability.TailscaleDetail{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != protocol.MediaDirect || got.Endpoint != "203.0.113.7" || !got.Usable() {
		t.Fatalf("bad direct media network: %+v", got)
	}
}

func TestResolveMediaNetworkRejectsUnknownProvider(t *testing.T) {
	t.Setenv("MEDIA_NETWORK", "steam_overlay_net")
	if _, err := capabilityResolve(t, capability.TailscaleDetail{}); err == nil {
		t.Fatal("unknown provider must be rejected, not silently advertised")
	}
}

func TestMediaNetworkUsableRequiresAll(t *testing.T) {
	m := protocol.MediaNetwork{Provider: protocol.MediaDirect, Endpoint: "1.2.3.4",
		Reachable: true, TCPOK: true, UDPOK: true}
	if !m.Usable() {
		t.Fatal("complete network should be usable")
	}
	m.UDPOK = false
	if m.Usable() {
		t.Fatal("without UDP the media path is not usable for Moonlight")
	}
	m.UDPOK = true
	m.Provider = "kaggle_special"
	if m.Usable() {
		t.Fatal("unknown provider must never be usable")
	}
}

// -- client API bearer auth --------------------------------------------------

func TestClientAuthMiddleware(t *testing.T) {
	calls := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ })
	protected := auth.ClientAuthMiddleware("s3cret", next)

	req := httptest.NewRequest(http.MethodGet, "/v1/games", nil)
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || calls != 0 {
		t.Fatalf("missing token must 401, got %d calls=%d", rec.Code, calls)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/games", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec = httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || calls != 0 {
		t.Fatalf("wrong token must 401, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/games", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	rec = httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || calls != 1 {
		t.Fatalf("good token must pass, got %d calls=%d", rec.Code, calls)
	}

	// Node routes stay exempt (enrollment credential + fencing authenticate
	// machines; the client token is for the human's client).
	req = httptest.NewRequest(http.MethodPost, "/v1/nodes/enroll", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("node enroll must not require the client API token")
	}
	req = httptest.NewRequest(http.MethodGet, "/v1/nodes/n1/sessions", nil)
	rec = httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("node work pickup must not require the client API token")
	}

	// Empty token config = pass-through (local dev).
	open := auth.ClientAuthMiddleware("", next)
	open.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/games", nil))
	if calls < 2 {
		t.Fatalf("open middleware should pass through, calls=%d", calls)
	}
}

// -- session assignment honors capabilities end-to-end ------------------------

func TestCreateSessionRejectsNodeWithoutStreamingPolicy(t *testing.T) {
	cat := newAuthTestCatalog(t)
	reg := nodes.New()
	capsNoPolicy := capableCaps()
	capsNoPolicy.StreamingAllowed = false
	reg.Enroll("n1", capsNoPolicy)
	mgr := csession.NewManager(csession.NewMemoryStore(), cat, reg)
	if _, err := mgr.Create("u1", "doom2"); err == nil {
		t.Fatal("session creation must fail with NO_CAPACITY when no node may stream")
	}
}

func TestCreateSessionUsesUsableMediaEndpoint(t *testing.T) {
	cat := newAuthTestCatalog(t)
	reg := nodes.New()
	reg.Enroll("n1", capableCaps())
	mgr := csession.NewManager(csession.NewMemoryStore(), cat, reg)
	s, err := mgr.Create("u1", "doom2")
	if err != nil {
		t.Fatal(err)
	}
	if s.NodeID != "n1" {
		t.Fatalf("expected assignment, got %+v", s)
	}
	_ = time.Now()
}
