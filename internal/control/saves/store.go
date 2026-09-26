// Package saves is the control-plane authority for persistent game state:
// generation issuance, fence-checked commit, latest-pointer moves, and
// scoped storage authorization. PostgreSQL owns ordering and pointers;
// object storage owns immutable blobs. Nodes never see storage secrets.
package saves

import (
	"fmt"
	"sort"
	"sync"

	"github.com/personal-game/personal-game/pkg/protocol"
)

// Store persists snapshots and the latest pointer. MemoryStore is the
// Stage 3 default; the PostgreSQL implementation plugs in here unchanged.
type Store interface {
	// IssuePending atomically allocates generation max+1 and inserts it PENDING.
	IssuePending(userID, gameID, nodeID, sessionID string, parent *uint64) (*protocol.SaveSnapshot, error)
	// Finalize moves PENDING -> VALID/CHECKPOINT with blob metadata.
	Finalize(userID, gameID string, gen uint64, to protocol.SaveState, sha string, size uint64, files int) (*protocol.SaveSnapshot, error)
	// Mark moves any generation to ORPHANED/CORRUPT (zombie/failed uploads).
	Mark(userID, gameID string, gen uint64, to protocol.SaveState) error
	// Get returns one generation.
	Get(userID, gameID string, gen uint64) (*protocol.SaveSnapshot, error)
	// Pointer returns the latest committed generation (0 = none).
	Pointer(userID, gameID string) (uint64, error)
	// Advance moves the pointer forward only (monotonic guard inside).
	Advance(userID, gameID string, gen uint64) error
	// Latest returns the snapshot the pointer references.
	Latest(userID, gameID string) (*protocol.SaveSnapshot, error)
	// History returns newest-first snapshots, most recent `limit`.
	History(userID, gameID string, limit int) ([]*protocol.SaveSnapshot, error)
}

// MemoryStore is a mutex-guarded in-memory Store.
type MemoryStore struct {
	mu    sync.Mutex
	snaps map[string]*protocol.SaveSnapshot // user/game/gen -> snapshot
	point map[string]uint64                 // user/game -> latest gen
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{snaps: map[string]*protocol.SaveSnapshot{}, point: map[string]uint64{}}
}

func k(userID, gameID string, gen uint64) string {
	return userID + "\x00" + gameID + "\x00" + fmt.Sprintf("%020d", gen)
}

func pk(userID, gameID string) string { return userID + "\x00" + gameID }

// IssuePending implements Store.
func (m *MemoryStore) IssuePending(userID, gameID, nodeID, sessionID string, parent *uint64) (*protocol.SaveSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var max uint64
	for key, s := range m.snaps {
		_ = key
		if s.UserID == userID && s.GameID == gameID && s.Generation > max {
			max = s.Generation
		}
	}
	s := &protocol.SaveSnapshot{
		SchemaVersion: protocol.SaveSchemaVersion,
		UserID:        userID,
		GameID:        gameID,
		Generation:    max + 1,
		NodeID:        nodeID,
		SessionID:     sessionID,
		State:         protocol.SavePending,
	}
	if parent != nil {
		p := *parent
		s.ParentGeneration = &p
	}
	cp := *s
	m.snaps[k(userID, gameID, s.Generation)] = &cp
	return &cp, nil
}

// Finalize implements Store.
func (m *MemoryStore) Finalize(userID, gameID string, gen uint64, to protocol.SaveState, sha string, size uint64, files int) (*protocol.SaveSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.snaps[k(userID, gameID, gen)]
	if !ok {
		return nil, fmt.Errorf("saves: unknown generation %d", gen)
	}
	if s.State != protocol.SavePending {
		return nil, fmt.Errorf("saves: generation %d not pending (state %s)", gen, s.State)
	}
	if to != protocol.SaveValid && to != protocol.SaveCheckpoint {
		return nil, fmt.Errorf("saves: invalid finalize state %s", to)
	}
	s.State = to
	s.SHA256 = sha
	s.SizeBytes = size
	s.FileCount = files
	cp := *s
	return &cp, nil
}

// Mark implements Store.
func (m *MemoryStore) Mark(userID, gameID string, gen uint64, to protocol.SaveState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.snaps[k(userID, gameID, gen)]
	if !ok {
		return fmt.Errorf("saves: unknown generation %d", gen)
	}
	s.State = to
	return nil
}

// Get implements Store.
func (m *MemoryStore) Get(userID, gameID string, gen uint64) (*protocol.SaveSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.snaps[k(userID, gameID, gen)]
	if !ok {
		return nil, fmt.Errorf("saves: unknown generation %d", gen)
	}
	cp := *s
	return &cp, nil
}

// Pointer implements Store.
func (m *MemoryStore) Pointer(userID, gameID string) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.point[pk(userID, gameID)], nil
}

// Advance implements Store: strictly forward, never backward.
func (m *MemoryStore) Advance(userID, gameID string, gen uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if gen <= m.point[pk(userID, gameID)] {
		return fmt.Errorf("saves: refusing to move pointer backward (%d <= current)", gen)
	}
	m.point[pk(userID, gameID)] = gen
	return nil
}

// Latest implements Store.
func (m *MemoryStore) Latest(userID, gameID string) (*protocol.SaveSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	gen := m.point[pk(userID, gameID)]
	if gen == 0 {
		return nil, fmt.Errorf("saves: no saves yet")
	}
	s, ok := m.snaps[k(userID, gameID, gen)]
	if !ok {
		return nil, fmt.Errorf("saves: pointer references missing generation %d", gen)
	}
	cp := *s
	return &cp, nil
}

// History implements Store.
func (m *MemoryStore) History(userID, gameID string, limit int) ([]*protocol.SaveSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*protocol.SaveSnapshot
	for _, s := range m.snaps {
		if s.UserID == userID && s.GameID == gameID {
			cp := *s
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Generation > out[j].Generation })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
