package postgres

import (
	"fmt"

	controlSaves "github.com/personal-game/personal-game/internal/control/saves"
	"github.com/personal-game/personal-game/pkg/protocol"
)

// Compile-time guarantee: SaveStore satisfies the control-plane contract,
// so swapping MemoryStore for PostgreSQL changes no callers.
var _ controlSaves.Store = (*SaveStore)(nil)

func errBadState(to protocol.SaveState) error {
	return fmt.Errorf("postgres: invalid finalize state %s", to)
}

func errNotPending(gen uint64) error {
	return fmt.Errorf("postgres: generation %d not pending", gen)
}

func errBackward(gen uint64) error {
	return fmt.Errorf("postgres: refusing to move pointer backward to %d", gen)
}

func errNone() error { return fmt.Errorf("postgres: no saves yet") }
