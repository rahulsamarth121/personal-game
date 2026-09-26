// Package auth defines the client-API authentication boundary.
//
// The control plane is a single-user personal service; when PG_API_TOKEN is
// set on the control plane, every client call (games, sessions, saves,
// playtime) must present that same value as `Authorization: Bearer <token>`.
// Node-machine routes (enroll/heartbeat/work pickup) authenticate with the
// node's enrollment credential / lease fencing instead and stay exempt here.
//
// Tokens are compared with subtle.ConstantTimeCompare, never logged, and
// never accepted from query strings. An unset PG_API_TOKEN keeps the local
// dev/open behavior (loopback-first single-user system) — the setting is
// opt-in hardening, not a new dependency.
package auth

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
)

// Claims is the minimal identity the control plane trusts.
type Claims struct {
	UserID string
	Expiry int64 // unix seconds; 0 = no expiry check in stage 0
}

// Verifier validates a bearer token into Claims.
type Verifier interface {
	Verify(token string) (Claims, error)
}

// StaticVerifier accepts one pre-shared token (personal deployment).
// Production multi-user systems replace it; it exists so the API compiles
// and runs, and so personal deployments can harden with one env var.
type StaticVerifier struct {
	UserID string
	Token  string
}

// Verify implements Verifier.
func (s StaticVerifier) Verify(token string) (Claims, error) {
	if s.Token == "" || token == "" {
		return Claims{}, errors.New("auth: token required")
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(s.Token)) != 1 {
		return Claims{}, errors.New("auth: invalid token")
	}
	return Claims{UserID: s.UserID}, nil
}

// nodePaths are machine-to-machine routes exempt from the client token:
// agents authenticate with their enrollment credential and lease fencing
// (separate mechanisms, per ADR-0004). Health checks stay open.
var nodePaths = map[string]bool{
	"/v1/nodes/enroll":    true,
	"/v1/nodes/heartbeat": true,
	"/healthz":            true,
}

func isNodePath(path string) bool {
	if nodePaths[path] {
		return true
	}
	// Work pickup: /v1/nodes/{id}/sessions — token-matched by the agent's
	// fence token on every subsequent control call.
	return strings.HasPrefix(path, "/v1/nodes/")
}

// ClientAuthMiddleware enforces bearer authentication on client API routes
// when token is non-empty. When token is empty the middleware is a
// pass-through (local dev). Node routes stay exempt (enrollment credential
// + fencing authenticate those); /healthz stays open.
func ClientAuthMiddleware(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	verifier := StaticVerifier{UserID: "owner", Token: token}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isNodePath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			w.Header().Set("WWW-Authenticate", `Bearer realm="personal-game"`)
			writeUnauthorized(w, "authentication required")
			return
		}
		claims, err := verifier.Verify(strings.TrimSpace(strings.TrimPrefix(h, "Bearer ")))
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="personal-game"`)
			writeUnauthorized(w, "invalid or missing API token")
			return
		}
		if claims.UserID != "" {
			r.Header.Set("X-PG-User", claims.UserID)
		}
		next.ServeHTTP(w, r)
	})
}

func writeUnauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error": "` + msg + `"}`))
}
