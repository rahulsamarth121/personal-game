package unit

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/personal-game/personal-game/internal/agent/capability"
	"github.com/personal-game/personal-game/internal/agent/session"
	"github.com/personal-game/personal-game/internal/common"
	"github.com/personal-game/personal-game/internal/control/api"
	"github.com/personal-game/personal-game/internal/control/catalog"
	"github.com/personal-game/personal-game/internal/control/nodes"
	"github.com/personal-game/personal-game/internal/control/saves"
	csession "github.com/personal-game/personal-game/internal/control/session"
	"github.com/personal-game/personal-game/internal/store/object"
	"github.com/personal-game/personal-game/pkg/protocol"
)

func newAuditServer(t *testing.T, enrollToken string) *api.Server {
	t.Helper()
	cat := catalog.New()
	nreg := nodes.New()
	mgr := csession.NewManager(csession.NewMemoryStore(), cat, nreg)
	sm := saves.NewManager(saves.NewMemoryStore(), object.NewMemoryStore(), mgr)
	srv := api.NewFull(cat, nreg, mgr, sm)
	srv.EnrollToken = enrollToken
	return srv
}

// The Kaggle failure mode: an operator sets PG_ENROLL_TOKEN on the control
// plane but the agent presents none (or the wrong one). Enrollment must be
// rejected with 401 — never silently registered.
func TestEnrollRequiresTokenWhenConfigured(t *testing.T) {
	const token = "audit-enroll-secret-123"
	srv := newAuditServer(t, token)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	caps := capability.Discover()
	caps.StreamingAllowed = false

	post := func(enrollToken string) int {
		// The token travels in the request BODY (EnrollRequest.enroll_token),
		// exactly as internal/agent/lifecycle sends it — never a URL/query param.
		body, _ := json.Marshal(protocol.EnrollRequest{
			NodeID: "n1", EnrollToken: enrollToken, Caps: caps})
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/nodes/enroll",
			bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if got := post(""); got != http.StatusUnauthorized {
		t.Fatalf("missing enroll token: got %d, want 401", got)
	}
	if got := post("wrong-token"); got != http.StatusUnauthorized {
		t.Fatalf("wrong enroll token: got %d, want 401", got)
	}
	if got := post(token); got != http.StatusOK {
		t.Fatalf("correct enroll token: got %d, want 200", got)
	}
}

func TestEnrollOpenWhenNoTokenConfigured(t *testing.T) {
	srv := newAuditServer(t, "")
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	body, _ := json.Marshal(protocol.EnrollRequest{
		NodeID: "n1", Caps: capability.Discover()})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/nodes/enroll",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("open enroll: got %d, want 200", resp.StatusCode)
	}
}

// Agent-side: when the environment carries PG_API_TOKEN, the session
// control client must present it as a bearer header on client routes.
func TestAgentControlSendsBearerWhenTokenConfigured(t *testing.T) {
	const token = "audit-api-token-456"
	var gotAuth, gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(ts.Close)

	t.Setenv("PG_API_TOKEN", token)
	ctl := session.NewControl(ts.URL)
	if _, err := ctl.GetSession(context.Background(), "s1"); err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if gotPath != "/v1/sessions/s1" || gotAuth != "Bearer "+token {
		t.Fatalf("bearer header missing/wrong: path=%q auth=%q", gotPath, gotAuth)
	}

	// No token configured -> no Authorization header (open local-dev plane).
	t.Setenv("PG_API_TOKEN", "")
	ctl2 := session.NewControl(ts.URL)
	if _, err := ctl2.GetSession(context.Background(), "s1"); err != nil {
		t.Fatalf("GetSession(no token): %v", err)
	}
	if gotAuth != "" {
		t.Fatalf("unexpected auth header without token: %q", gotAuth)
	}
}

// A malformed control URL must be rejected before any network I/O.
func TestAgentConfigRejectsURLWithoutScheme(t *testing.T) {
	t.Setenv("PG_CONTROL_URL", "personal-game-relay.example.workers.dev/personal-game")
	t.Setenv("PG_DATA_DIR", t.TempDir())
	cfg, err := common.LoadAgentConfig()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if verr := cfg.Validate(); verr == nil {
		t.Fatal("expected scheme validation error, got nil")
	}
	t.Setenv("PG_CONTROL_URL", "https://personal-game-relay.example.workers.dev/personal-game")
	cfg2, err := common.LoadAgentConfig()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if verr := cfg2.Validate(); verr != nil {
		t.Fatalf("valid https URL rejected: %v", verr)
	}
}
