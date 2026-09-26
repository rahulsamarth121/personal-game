// Package session implements the control-plane session state machine and
// fencing-token rules. Pure logic, no I/O: easy to unit test.
package session

import (
	"errors"

	"github.com/personal-game/personal-game/internal/common"
	"github.com/personal-game/personal-game/pkg/protocol"
)

// Transition validates from -> to.
func Transition(from, to protocol.SessionState) error {
	if from.CanTransition(to) {
		return nil
	}
	return common.E(common.CodeConflict,
		"illegal session transition "+string(from)+" -> "+string(to), nil)
}

// Lease groups the fencing token with its session. Higher token wins;
// a node holding a stale token is a zombie and must be fenced.
type Lease struct {
	SessionID string
	NodeID    string
	Token     uint64
}

// IsZombie reports whether holder (oldToken) lost to current.
func IsZombie(currentToken, holderToken uint64) bool {
	return holderToken != currentToken
}

// CheckFence errors when holderToken != currentToken.
func CheckFence(currentToken, holderToken uint64) error {
	if holderToken != currentToken {
		return common.E(common.CodeFenced, "stale fence token: zombie node must not commit", nil)
	}
	return nil
}

// CommitSaveParams are the inputs to the save-pointer advance decision.
type CommitSaveParams struct {
	CurrentToken uint64
	HolderToken  uint64
	LatestGen    uint64
	NewGen       uint64
}

// DecideCommit enforces: fence match AND strictly increasing generation.
// On fence mismatch the blob must be marked ORPHANED, never VALID.
func DecideCommit(p CommitSaveParams) error {
	if p.HolderToken != p.CurrentToken {
		return common.E(common.CodeFenced, "fence mismatch: refuse to advance save pointer", nil)
	}
	if p.NewGen <= p.LatestGen {
		return common.E(common.CodeConflict, "generation must increase", errors.New(
			"latest="+utoa(p.LatestGen)+" new="+utoa(p.NewGen)))
	}
	return nil
}

func utoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
