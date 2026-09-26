package session

import (
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
	"time"

	"github.com/personal-game/personal-game/pkg/protocol"
)

// Store persists sessions and events. MemoryStore is the Stage 2 default
// (PostgreSQL implementation plugs in here without changing callers; the
// schema already exists in migrations/0001_init.sql).
type Store interface {
	Save(s *protocol.Session) error
	Get(id string) (*protocol.Session, error)
	List(userID string) ([]*protocol.Session, error)
	AppendEvent(sessionID, event string, detail map[string]any) error
}

// MemoryStore is a mutex-guarded in-memory Store for dev/tests.
type MemoryStore struct {
	mu       sync.Mutex
	sessions map[string]*protocol.Session
	events   map[string][]Event
}

// Event is one history row.
type Event struct {
	At     time.Time
	Event  string
	Detail map[string]any
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{sessions: map[string]*protocol.Session{}, events: map[string][]Event{}}
}

// Save implements Store (upsert by session id).
func (m *MemoryStore) Save(s *protocol.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *s
	m.sessions[s.SessionID] = &cp
	return nil
}

// Get implements Store.
func (m *MemoryStore) Get(id string) (*protocol.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, errNotFound()
	}
	cp := *s
	return &cp, nil
}

// List implements Store (empty userID lists all; newest first).
func (m *MemoryStore) List(userID string) ([]*protocol.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*protocol.Session
	for _, s := range m.sessions {
		if userID != "" && s.UserID != userID {
			continue
		}
		cp := *s
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// AppendEvent implements Store.
func (m *MemoryStore) AppendEvent(sessionID, event string, detail map[string]any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events[sessionID] = append(m.events[sessionID], Event{At: time.Now(), Event: event, Detail: detail})
	return nil
}

// NewID mints a 128-bit random session id (hex).
func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
