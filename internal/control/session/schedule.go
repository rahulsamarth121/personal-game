package session

import (
	"sort"
	"time"

	"github.com/personal-game/personal-game/internal/control/nodes"
	"github.com/personal-game/personal-game/pkg/protocol"
)

// PickNode selects the best node for requirements from a snapshot.
// Eligibility is capability-driven, never provider-name-driven:
//   - healthy (not DEAD/SUSPECT-liable stale lease) and agent-side IDLE/PREPARING
//   - StreamingAllowed: the operator policy bit — a node without it can
//     host nothing interactive, so it is skipped before hardware checks
//   - capabilities satisfy the manifest (VRAM, gamepad, HDR)
//   - a usable media network (measured endpoint the client can reach)
//   - a real streaming backend available (Wolf or Sunshine)
//
// Ranking prefers a matching GPU vendor model hint, then more VRAM, then
// most-recently-seen (fresher lease = safer). Deterministic and simple;
// no global optimizer. Returns nil when nothing qualifies — the caller
// fails the session clearly instead of assigning a node that cannot run it.
func PickNode(all []*nodes.Node, req protocol.CapabilityRequirements, now time.Time) *nodes.Node {
	cands := make([]*nodes.Node, 0, len(all))
	for _, n := range all {
		if n == nil || n.Health == protocol.NodeDead {
			continue
		}
		if n.State != protocol.NodeIdle && n.State != protocol.NodePreparing {
			continue
		}
		if err := n.Caps.Satisfies(req); err != nil {
			// Includes StreamingAllowed=false and VRAM/gamepad/HDR gaps.
			continue
		}
		if !n.Caps.Network.MediaNetwork.Usable() {
			continue
		}
		if len(n.Caps.Streaming) == 0 {
			continue
		}
		cands = append(cands, n)
	}
	if len(cands) == 0 {
		return nil
	}
	sort.Slice(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		// Prefer the node with more VRAM when both report a GPU (cheap,
		// deterministic stand-in for "best fit" without an optimizer).
		if a.Caps.GPU.VRAMMB != b.Caps.GPU.VRAMMB {
			return a.Caps.GPU.VRAMMB > b.Caps.GPU.VRAMMB
		}
		return a.LastSeen.After(b.LastSeen)
	})
	return cands[0]
}

// UnavailableReason explains in operator terms why nothing was schedulable,
// for honest session-failure messages (never exposing node internals).
func UnavailableReason(all []*nodes.Node) string {
	if len(all) == 0 {
		return "no gaming nodes are registered"
	}
	var noPolicy, noBackend, noMedia, hw int
	for _, n := range all {
		switch {
		case !n.Caps.StreamingAllowed:
			noPolicy++
		case len(n.Caps.Streaming) == 0:
			noBackend++
		case !n.Caps.Network.MediaNetwork.Usable():
			noMedia++
		default:
			hw++
		}
	}
	switch {
	case noPolicy == len(all):
		return "registered nodes are not authorized for game streaming (set STREAMING_ALLOWED on a permitted node)"
	case noBackend == len(all):
		return "registered nodes have no streaming backend (Wolf or Sunshine) available"
	case noMedia == len(all):
		return "registered nodes have no usable media network endpoint yet"
	default:
		return "all capable nodes are busy or offline"
	}
}
