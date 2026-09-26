package unit

import (
	"testing"

	"github.com/personal-game/personal-game/internal/common"
	"github.com/personal-game/personal-game/internal/control/session"
	"github.com/personal-game/personal-game/pkg/protocol"
)

func TestSessionLegalPath(t *testing.T) {
	path := []protocol.SessionState{
		protocol.SessionRequested, protocol.SessionNodeAssigned,
		protocol.SessionPreparing, protocol.SessionReady,
		protocol.SessionStreaming, protocol.SessionDraining,
		protocol.SessionClosed,
	}
	for i := 0; i+1 < len(path); i++ {
		if err := session.Transition(path[i], path[i+1]); err != nil {
			t.Fatalf("legal transition %s -> %s rejected: %v", path[i], path[i+1], err)
		}
	}
}

func TestSessionIllegalSkips(t *testing.T) {
	if err := session.Transition(protocol.SessionRequested, protocol.SessionStreaming); err == nil {
		t.Fatal("expected REQUESTED -> STREAMING to be illegal")
	}
	if err := session.Transition(protocol.SessionClosed, protocol.SessionStreaming); err == nil {
		t.Fatal("expected CLOSED -> STREAMING to be illegal")
	}
}

func TestClientDisconnectGrace(t *testing.T) {
	// STREAMING -> DEGRADED -> DRAINING keeps the game alive across a blip.
	for _, tr := range [][2]protocol.SessionState{
		{protocol.SessionStreaming, protocol.SessionDegraded},
		{protocol.SessionDegraded, protocol.SessionDraining},
		{protocol.SessionDraining, protocol.SessionClosed},
	} {
		if err := session.Transition(tr[0], tr[1]); err != nil {
			t.Fatalf("%s -> %s should be legal: %v", tr[0], tr[1], err)
		}
	}
}

func TestZombieFence(t *testing.T) {
	// Node A saved gen 41 with token 42; control moved on to token 43 / gen 42.
	// A's commit must be refused and its blob orphaned, never VALID.
	if err := session.DecideCommit(session.CommitSaveParams{
		CurrentToken: 43, HolderToken: 42, LatestGen: 42, NewGen: 42,
	}); err == nil {
		t.Fatal("zombie commit must be refused")
	} else if ce, ok := err.(*common.AppError); !ok || ce.Code != common.CodeFenced {
		t.Fatalf("expected FENCED error, got %v", err)
	}
	if err := session.DecideCommit(session.CommitSaveParams{
		CurrentToken: 43, HolderToken: 43, LatestGen: 42, NewGen: 43,
	}); err != nil {
		t.Fatalf("legit successor commit refused: %v", err)
	}
	if err := session.DecideCommit(session.CommitSaveParams{
		CurrentToken: 43, HolderToken: 43, LatestGen: 42, NewGen: 42,
	}); err == nil {
		t.Fatal("non-increasing generation must be refused")
	}
}

func TestNodeCommandEnumClosed(t *testing.T) {
	for _, c := range []protocol.NodeCommand{
		protocol.CmdAcquire, protocol.CmdRestore, protocol.CmdLaunch,
		protocol.CmdSnapshot, protocol.CmdTerminate, protocol.CmdEvict,
	} {
		if !c.IsValid() {
			t.Fatalf("command %s should be valid", c)
		}
	}
	if protocol.NodeCommand("EXEC rm -rf /").IsValid() {
		t.Fatal("arbitrary EXEC must never be a valid command")
	}
}
