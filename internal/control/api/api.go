// Package api wires the HTTP surface:
// GET /healthz, GET /v1/games, POST /v1/nodes/enroll, POST /v1/nodes/heartbeat,
// POST /v1/sessions, GET /v1/sessions, GET /v1/sessions/{id},
// DELETE /v1/sessions/{id}, GET /v1/stats/playtime.
// Save routes land in Stage 3; the router shape is fixed here.
package api

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/personal-game/personal-game/internal/control/catalog"
	"github.com/personal-game/personal-game/internal/control/nodes"
	"github.com/personal-game/personal-game/internal/control/saves"
	"github.com/personal-game/personal-game/internal/control/session"
	"github.com/personal-game/personal-game/internal/store/object"
	"github.com/personal-game/personal-game/pkg/protocol"
)

// Server holds API dependencies.
type Server struct {
	Catalog  *catalog.Registry
	Nodes    *nodes.Registry
	Sessions *session.Manager
	Saves    *saves.Manager
	mux      *http.ServeMux

	cmdMu    sync.Mutex
	commands map[string][]NodeCommandReq // nodeID -> pending commands
	cmdSeq   uint64

	// EnrollToken, when set, is required (constant-time compare) in every
	// EnrollRequest. Empty keeps the open local-dev behavior — same policy
	// as the client API token. Never logged, never echoed in responses.
	EnrollToken string
}

// NodeCommandReq is one queued instruction for a node agent. Agents poll
// and ack; the queue is memory-resident (single-user control plane).
type NodeCommandReq struct {
	ID        string               `json:"id"`
	Command   protocol.NodeCommand `json:"command"`
	GameID    string               `json:"game_id,omitempty"`
	CreatedAt time.Time            `json:"created_at"`
}

// New builds the router.
func New(cat *catalog.Registry, n *nodes.Registry) *Server {
	return NewWithSessions(cat, n, session.NewManager(session.NewMemoryStore(), cat, n))
}

// NewWithSessions builds the router with an explicit session manager
// (tests inject a fake clock) and memory save/object stores.
func NewWithSessions(cat *catalog.Registry, n *nodes.Registry, mgr *session.Manager) *Server {
	sm := saves.NewManager(saves.NewMemoryStore(), object.NewMemoryStore(), mgr)
	return NewFull(cat, n, mgr, sm)
}

// NewFull builds the router with fully injected managers (production
// wires PostgreSQL/R2-backed stores here).
func NewFull(cat *catalog.Registry, n *nodes.Registry, mgr *session.Manager, sm *saves.Manager) *Server {
	s := &Server{Catalog: cat, Nodes: n, Sessions: mgr, Saves: sm,
		mux: http.NewServeMux(), commands: map[string][]NodeCommandReq{}}
	s.EnrollToken = os.Getenv("PG_ENROLL_TOKEN")
	s.Saves.OnCommitted = mgr.NoteSaveGen
	s.mux.HandleFunc("/healthz", s.healthz)
	s.mux.HandleFunc("/v1/games", s.games)
	s.mux.HandleFunc("/v1/games/", s.gameByID)
	s.mux.HandleFunc("/v1/nodes/enroll", s.enroll)
	s.mux.HandleFunc("/v1/nodes/heartbeat", s.heartbeat)
	s.mux.HandleFunc("/v1/nodes/", s.nodeByID)
	s.mux.HandleFunc("/v1/sessions", s.sessions)
	s.mux.HandleFunc("/v1/sessions/", s.sessionByID)
	s.mux.HandleFunc("/v1/stats/playtime", s.playtime)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) games(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"games": s.Catalog.List()})
	case http.MethodPost:
		// GUI/CLI game onboarding: the Go validator is the single source
		// of truth (upsert by game_id so re-adding updates the entry).
		var m protocol.GameManifest
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		if err := s.Catalog.Put(m); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		// Durable onboarding: persist the validated manifest into the seed
		// directory so GUI-added games survive a restart. Failure to persist
		// is reported but the game stays usable for this process lifetime.
		persisted := true
		if dir := manifestDir(); dir != "" {
			if err := s.Catalog.SaveToFile(m.GameID, dir); err != nil {
				persisted = false
				fmt.Fprintf(os.Stderr, "api: manifest persist failed (in-memory only): %v\n", err)
			}
		}
		writeJSON(w, http.StatusCreated, map[string]any{"manifest": m, "persisted": persisted})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// gameByID handles GET /v1/games/{id}[/manifest], GET
// /v1/games/{id}/saves/latest?user_id= (save status without a session),
// and POST /v1/games/{id}/evict (queue installed-game eviction on nodes).
func (s *Server) gameByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/games/")
	id, sub, _ := strings.Cut(rest, "/")
	if id == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown route"})
		return
	}
	m, err := s.Catalog.Get(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "game not found"})
		return
	}
	switch {
	case sub == "" || sub == "manifest":
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		writeJSON(w, http.StatusOK, m)
	case sub == "saves/latest" && r.Method == http.MethodGet:
		userID := r.URL.Query().Get("user_id")
		if userID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id required"})
			return
		}
		snap, err := s.Saves.Saves.Latest(userID, id)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no saves yet"})
			return
		}
		writeJSON(w, http.StatusOK, snap)
	case sub == "evict" && r.Method == http.MethodPost:
		nodes := s.Nodes.Snapshot()
		if len(nodes) == 0 {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no nodes registered"})
			return
		}
		s.cmdMu.Lock()
		var targets []string
		for _, n := range nodes {
			s.cmdSeq++
			req := NodeCommandReq{
				ID: fmt.Sprintf("evict-%d", s.cmdSeq), Command: protocol.CmdEvict,
				GameID: id, CreatedAt: time.Now(),
			}
			s.commands[n.NodeID] = append(s.commands[n.NodeID], req)
			targets = append(targets, n.NodeID)
		}
		s.cmdMu.Unlock()
		writeJSON(w, http.StatusAccepted, map[string]any{"evict": id, "nodes": targets})
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown route"})
	}
}

func (s *Server) enroll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var req protocol.EnrollRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.NodeID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "node_id required"})
		return
	}
	if s.EnrollToken != "" {
		presented := req.EnrollToken
		if subtle.ConstantTimeCompare([]byte(presented), []byte(s.EnrollToken)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid enrollment token"})
			return
		}
	}
	n, prev := s.Nodes.Enroll(req.NodeID, req.Caps)
	if prev != 0 {
		// Re-enrollment supersedes the old token: fence sessions the
		// previous agent instance may still believe it owns.
		s.Sessions.FenceStaleSessions(req.NodeID, n.FenceToken)
	}
	writeJSON(w, http.StatusOK, protocol.EnrollResponse{
		NodeID: n.NodeID, FenceToken: n.FenceToken,
		LeaseTTLSecs: int(s.Nodes.LeaseTTL().Seconds()),
	})
}

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var req protocol.HeartbeatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.NodeID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "node_id required"})
		return
	}
	n := s.Nodes.HeartbeatWithToken(req.NodeID, req.FenceToken, true)
	if n == nil {
		// Unknown node or stale token: zombie. 409 so the agent stops work
		// and re-enrolls instead of continuing under a dead lease.
		writeJSON(w, http.StatusConflict, protocol.HeartbeatResponse{Fenced: true})
		return
	}
	writeJSON(w, http.StatusOK, protocol.HeartbeatResponse{
		FenceToken: n.FenceToken, LeaseTTLSecs: int(s.Nodes.LeaseTTL().Seconds()),
	})
}

// nodeByID handles GET /v1/nodes/{id}/sessions: work pickup for the agent.
// Only the node's own pending sessions are listed (assignment + token
// match happen agent-side before any work starts).
func (s *Server) nodeByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/v1/nodes/")
	id, sub, _ := strings.Cut(rest, "/")
	if id == "" || sub != "sessions" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown route"})
		return
	}
	list, err := s.Sessions.SessionsForNode(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": list})
}

type createSessionRequest struct {
	UserID string `json:"user_id"`
	GameID string `json:"game_id"`
}

// sessions handles POST /v1/sessions (create) and GET /v1/sessions (list).
func (s *Server) sessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var req createSessionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		sess, err := s.Sessions.Create(req.UserID, req.GameID)
		if err != nil {
			writeJSON(w, statusFor(err), map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, sess)
	case http.MethodGet:
		list, err := s.Sessions.Store.List(r.URL.Query().Get("user_id"))
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"sessions": list})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// sessionByID handles GET /v1/sessions/{id}, DELETE /v1/sessions/{id},
// and the nested save routes POST .../saves/begin, POST .../saves/commit,
// GET .../saves/latest.
func (s *Server) sessionByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/sessions/")
	id, sub, _ := strings.Cut(rest, "/")
	if id == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown route"})
		return
	}
	switch sub {
	case "saves/begin":
		s.saveBegin(w, r, id)
		return
	case "saves/commit":
		s.saveCommit(w, r, id)
		return
	case "saves/latest":
		s.saveLatest(w, r, id)
		return
	case "observe":
		s.sessionObserve(w, r, id)
		return
	case "stream":
		s.sessionStream(w, r, id)
		return
	case "":
		// session routes below
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown route"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		sess, err := s.Sessions.Store.Get(id)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
			return
		}
		writeJSON(w, http.StatusOK, sess)
	case http.MethodDelete:
		// Close is fence-checked: caller proves the live holder's identity.
		nodeID := r.URL.Query().Get("node_id")
		var token uint64
		if v := r.URL.Query().Get("fence_token"); v != "" {
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid fence_token"})
				return
			}
			token = n
		}
		reason := protocol.SessionEndReason(r.URL.Query().Get("reason"))
		if reason == "" {
			reason = protocol.EndAborted
		}
		sess, err := s.Sessions.Close(id, nodeID, token, reason)
		if err != nil {
			writeJSON(w, statusFor(err), map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, sess)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

type saveBeginRequest struct {
	NodeID     string `json:"node_id"`
	FenceToken uint64 `json:"fence_token"`
}

// saveBegin handles POST /v1/sessions/{id}/saves/begin.
func (s *Server) saveBegin(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var req saveBeginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	grant, err := s.Saves.Begin(id, req.NodeID, req.FenceToken)
	if err != nil {
		writeJSON(w, statusFor(err), map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, grant)
}

type saveCommitRequest struct {
	NodeID     string `json:"node_id"`
	FenceToken uint64 `json:"fence_token"`
	Generation uint64 `json:"generation"`
	SHA256     string `json:"sha256"`
	SizeBytes  uint64 `json:"size_bytes"`
	FileCount  int    `json:"file_count"`
	Checkpoint bool   `json:"checkpoint"`
}

// saveCommit handles POST /v1/sessions/{id}/saves/commit.
func (s *Server) saveCommit(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var req saveCommitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	snap, err := s.Saves.Commit(saves.CommitParams{
		SessionID: id, NodeID: req.NodeID, Token: req.FenceToken,
		Generation: req.Generation, SHA256: req.SHA256,
		SizeBytes: req.SizeBytes, FileCount: req.FileCount,
		Checkpoint: req.Checkpoint,
	})
	if err != nil {
		writeJSON(w, statusFor(err), map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// saveLatest handles GET /v1/sessions/{id}/saves/latest: the session's
// user/game latest snapshot plus a scoped download URL.
func (s *Server) saveLatest(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	sess, err := s.Sessions.Store.Get(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	snap, dlURL, err := s.Saves.LatestGrant(sess.UserID, sess.GameID)
	if err != nil {
		writeJSON(w, statusFor(err), map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"snapshot": snap, "download_url": dlURL})
}

type observeRequest struct {
	NodeID     string                `json:"node_id"`
	FenceToken uint64                `json:"fence_token"`
	State      protocol.SessionState `json:"state"`
	GameActive bool                  `json:"game_active"`
}

// sessionObserve handles POST /v1/sessions/{id}/observe: the node's
// server-side state reports (drives READY/STREAMING/DEGRADED for real and
// accrues playtime). Fence-checked; zombies get 409.
func (s *Server) sessionObserve(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var req observeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	sess, err := s.Sessions.Observe(id, req.NodeID, req.FenceToken, req.State, req.GameActive)
	if err != nil {
		writeJSON(w, statusFor(err), map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

type streamRequest struct {
	NodeID     string `json:"node_id"`
	FenceToken uint64 `json:"fence_token"`
	Provider   string `json:"provider"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	App        string `json:"app"`
}

// sessionStream handles POST /v1/sessions/{id}/stream: the node publishes
// Moonlight connection info after verifying backend readiness.
func (s *Server) sessionStream(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var req streamRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.Host == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "host required"})
		return
	}
	sess, err := s.Sessions.SetStream(id, req.NodeID, req.FenceToken, protocol.StreamConfig{
		Provider: req.Provider, Host: req.Host, Port: req.Port, App: req.App,
	})
	if err != nil {
		writeJSON(w, statusFor(err), map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

// playtime handles GET /v1/stats/playtime?user_id=.
func (s *Server) playtime(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	stats, err := s.Sessions.Playtime(r.URL.Query().Get("user_id"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"playtime": stats})
}

// manifestDir resolves the durable seed directory for POSTed manifests.
// Tests (and other embedders) can pin it via PG_MANIFEST_DIR; production
// uses the same ./games/manifests the boot seed reads.
func manifestDir() string {
	if dir := os.Getenv("PG_MANIFEST_DIR"); dir != "" {
		return dir
	}
	return "./games/manifests"
}

// statusFor maps structured errors to HTTP codes without leaking internals.
func statusFor(err error) int {
	msg := err.Error()
	switch {
	case contains(msg, "FENCED"):
		return http.StatusConflict
	case contains(msg, "NO_CAPACITY"):
		return http.StatusServiceUnavailable
	case contains(msg, "NOT_FOUND"):
		return http.StatusNotFound
	case contains(msg, "INVALID_INPUT"):
		return http.StatusBadRequest
	default:
		return http.StatusConflict
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
