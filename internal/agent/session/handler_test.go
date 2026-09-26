package session

import (
	"context"
	"testing"
	"time"

	"github.com/personal-game/personal-game/internal/agent/backend"
	"github.com/personal-game/personal-game/pkg/protocol"
)

func TestClassifyExit(t *testing.T) {
	if got := classifyExit(backend.Status{Running: false, ExitCode: 0}); got != protocol.EndGameExit {
		t.Fatalf("clean exit: %s", got)
	}
	if got := classifyExit(backend.Status{Running: false, ExitCode: 1}); got != protocol.EndGameCrash {
		t.Fatalf("crash: %s", got)
	}
	if got := classifyExit(backend.Status{Running: false, BackendSessID: "wolf-9", ExitCode: -1}); got != protocol.EndClientGone {
		t.Fatalf("vanished stream: %s", got)
	}
}

func TestWaitRunningTimeout(t *testing.T) {
	// Fake backend that never starts: the handler must time out loudly,
	// never report STREAMING it did not observe.
	fb := backend.NewFakeBackend()
	h := &Handler{
		Control:      NewControl("http://127.0.0.1:9"),
		Backend:      fb,
		PollInterval: 20 * time.Millisecond,
		ReadyTimeout: 150 * time.Millisecond,
	}
	game := backend.Game{Host: "h"}
	game.Manifest.GameID = "g"
	err := h.waitRunning(context.Background(), "sid", "n", 1, game)
	if err == nil {
		t.Fatal("backend that never runs must time out, not stream")
	}
}
