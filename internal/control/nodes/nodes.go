// Package nodes tracks node registry, health (SUSPECT/DEAD overlay), and
// fencing-token issuance. PostgreSQL persistence lands in Stage 2.
package nodes

import (
	"sync"
	"time"

	"github.com/personal-game/personal-game/pkg/protocol"
)

// Node is the control-plane view of one agent.
type Node struct {
	NodeID     string
	State      protocol.NodeState
	Health     protocol.NodeHealth
	Caps       protocol.Capabilities
	FenceToken uint64
	LastSeen   time.Time
}

// Registry is an in-memory node registry (foundation default).
// PostgreSQL persistence lands in Stage 2; the fencing semantics here are
// authoritative and move with it.
type Registry struct {
	mu       sync.Mutex
	nodes    map[string]*Node
	tokens   uint64
	leaseTTL time.Duration
}

// New returns an empty registry with a 30s default lease.
func New() *Registry { return NewWithLease(30 * time.Second) }

// NewWithLease returns an empty registry with the given lease TTL.
func NewWithLease(ttl time.Duration) *Registry {
	return &Registry{nodes: map[string]*Node{}, leaseTTL: ttl}
}

// LeaseTTL reports the lease duration heartbeats must renew within.
func (r *Registry) LeaseTTL() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.leaseTTL
}

// Enroll registers or re-registers a node and issues a fresh fence token.
// It returns the previous token (0 for first enrollment) so callers can
// fence sessions pinned to the superseded token: a re-enrolled node means
// the old agent instance is a potential zombie.
func (r *Registry) Enroll(nodeID string, caps protocol.Capabilities) (*Node, uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokens++
	n, ok := r.nodes[nodeID]
	if !ok {
		n = &Node{NodeID: nodeID}
		r.nodes[nodeID] = n
	}
	prev := n.FenceToken
	n.Caps = caps
	n.State = protocol.NodeIdle
	n.Health = protocol.NodeHealthy
	n.FenceToken = r.tokens
	n.LastSeen = time.Now()
	return n, prev
}

// Heartbeat refreshes LastSeen; marks SUSPECT/DEAD on staleness elsewhere.
func (r *Registry) Heartbeat(nodeID string) *Node {
	return r.HeartbeatWithToken(nodeID, 0, true)
}

// HeartbeatWithToken renews the lease. When checkToken is true and the
// presented token differs from the registry's, the node is a zombie: the
// returned node is nil and Fenced reports true. A re-enroll (not a
// heartbeat) is the only way back, which issues a fresh token.
func (r *Registry) HeartbeatWithToken(nodeID string, token uint64, checkToken bool) *Node {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.nodes[nodeID]
	if !ok {
		return nil
	}
	if checkToken && token != n.FenceToken {
		return nil
	}
	n.LastSeen = time.Now()
	n.Health = protocol.NodeHealthy
	return n
}

// Fenced reports whether token is stale for nodeID (unknown node counts as
// fenced: never trust an unregistered node with privileged work).
func (r *Registry) Fenced(nodeID string, token uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.nodes[nodeID]
	if !ok {
		return true
	}
	return token != n.FenceToken
}

// Expired reports whether the lease lapsed (control may reassign sessions).
func (r *Registry) Expired(nodeID string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.nodes[nodeID]
	if !ok {
		return true
	}
	return now.Sub(n.LastSeen) > r.leaseTTL
}

// Snapshot returns copies of all nodes for scheduling decisions.
func (r *Registry) Snapshot() []*Node {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Node, 0, len(r.nodes))
	for _, n := range r.nodes {
		cp := *n
		out = append(out, &cp)
	}
	return out
}

// MarkStale applies the SUSPECT/DEAD overlay based on age.
func MarkStale(n *Node, now time.Time, suspectAfter, deadAfter time.Duration) {
	age := now.Sub(n.LastSeen)
	switch {
	case age > deadAfter:
		n.Health = protocol.NodeDead
	case age > suspectAfter:
		n.Health = protocol.NodeSuspect
	default:
		n.Health = protocol.NodeHealthy
	}
}
