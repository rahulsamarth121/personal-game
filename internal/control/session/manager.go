package session

import (
	"errors"
	"sync"
	"time"

	"github.com/personal-game/personal-game/internal/common"
	"github.com/personal-game/personal-game/internal/control/catalog"
	"github.com/personal-game/personal-game/internal/control/nodes"
	"github.com/personal-game/personal-game/pkg/protocol"
)

func errNotFound() error { return errors.New("session: not found") }

// maxTickDelta caps one observation's accrual so a control-plane outage or
// lost heartbeat burst never books phantom hours. Disconnects stop active
// accrual after the grace window by design (DEGRADED -> DRAINING).
const maxTickDelta = 120 * time.Second

// Manager owns the session state machine, node assignment, and
// server-observed playtime. The client is never trusted for durations:
// only state + node observations advance the counters.
type Manager struct {
	Store   Store
	Catalog *catalog.Registry
	Nodes   *nodes.Registry
	Now     func() time.Time

	mu   sync.Mutex
	last map[string]time.Time // last observation per session
}

// NewManager builds a Manager; Now defaults to time.Now (tests inject).
func NewManager(store Store, cat *catalog.Registry, reg *nodes.Registry) *Manager {
	return &Manager{Store: store, Catalog: cat, Nodes: reg, Now: time.Now, last: map[string]time.Time{}}
}

// Create validates the game, schedules a capable node, and records
// REQUESTED -> NODE_ASSIGNED -> PREPARING. The node's current fence token
// is pinned to the session; every later mutation revalidates it.
func (m *Manager) Create(userID, gameID string) (*protocol.Session, error) {
	if userID == "" || gameID == "" {
		return nil, common.E(common.CodeInvalidInput, "user_id and game_id required", nil)
	}
	manifest, err := m.Catalog.Get(gameID)
	if err != nil {
		return nil, common.E(common.CodeNotFound, "unknown game", err)
	}
	now := m.Now()
	node := PickNode(m.Nodes.Snapshot(), manifest.CapabilitiesRequired, now)
	if node == nil {
		return nil, common.E(common.CodeNoCapacity, "no capable node available", nil)
	}
	s := &protocol.Session{
		SchemaVersion: protocol.SessionSchemaVersion,
		SessionID:     NewID(),
		UserID:        userID,
		GameID:        gameID,
		NodeID:        node.NodeID,
		FenceToken:    node.FenceToken,
		State:         protocol.SessionRequested,
		CreatedAt:     now,
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	for _, next := range []protocol.SessionState{
		protocol.SessionNodeAssigned, protocol.SessionPreparing,
	} {
		if err := Transition(s.State, next); err != nil {
			return nil, err
		}
		s.State = next
	}
	m.mu.Lock()
	m.last[s.SessionID] = now
	m.mu.Unlock()
	if err := m.Store.Save(s); err != nil {
		return nil, err
	}
	_ = m.Store.AppendEvent(s.SessionID, "created", map[string]any{"node": node.NodeID})
	return s, nil
}

// Observe records a node heartbeat for a session and accrues time.
// fenceToken must match the session's pinned token; a zombie node's
// observations are rejected and accrue nothing. gameActive should reflect
// the supervised game process (not client claims).
func (m *Manager) Observe(sessionID, nodeID string, fenceToken uint64, state protocol.SessionState, gameActive bool) (*protocol.Session, error) {
	s, err := m.Store.Get(sessionID)
	if err != nil {
		return nil, common.E(common.CodeNotFound, "unknown session", err)
	}
	if s.State.IsTerminal() {
		return s, nil
	}
	if nodeID != s.NodeID || fenceToken != s.FenceToken {
		return nil, common.E(common.CodeFenced, "stale fence token: zombie observation refused", nil)
	}
	now := m.Now()
	m.mu.Lock()
	last, ok := m.last[sessionID]
	if !ok {
		last = s.CreatedAt
	}
	m.mu.Unlock()
	delta := now.Sub(last)
	if delta < 0 {
		delta = 0
	}
	if delta > maxTickDelta {
		delta = maxTickDelta
	}
	secs := int64(delta.Seconds())
	switch {
	case s.State == protocol.SessionPreparing || s.State == protocol.SessionReady:
		s.PrepareSeconds += secs
	case s.State.IsActive() && gameActive:
		s.ActiveSeconds += secs
	}
	if state != "" && state != s.State {
		if err := Transition(s.State, state); err != nil {
			return nil, err
		}
		s.State = state
		if state == protocol.SessionStreaming && s.ReadyAt == nil {
			t := now
			s.ReadyAt = &t
		}
	}
	s.WallSeconds = int64(now.Sub(s.CreatedAt).Seconds())
	m.mu.Lock()
	m.last[sessionID] = now
	m.mu.Unlock()
	if err := m.Store.Save(s); err != nil {
		return nil, err
	}
	return s, nil
}

// Close ends a session from the inside out: game-exit/crash paths drain
// first (DRAINING) so a final save can land, then CLOSED with the end
// reason and final counters. Fence is revalidated: zombies cannot close
// (or rewrite history of) sessions they lost.
func (m *Manager) Close(sessionID, nodeID string, fenceToken uint64, reason protocol.SessionEndReason) (*protocol.Session, error) {
	s, err := m.Store.Get(sessionID)
	if err != nil {
		return nil, common.E(common.CodeNotFound, "unknown session", err)
	}
	if s.State.IsTerminal() {
		return s, nil
	}
	if nodeID != s.NodeID || fenceToken != s.FenceToken {
		return nil, common.E(common.CodeFenced, "stale fence token: zombie close refused", nil)
	}
	now := m.Now()
	// Walk the legal path to terminal instead of jumping: active states
	// drain first; draining/failed-adjacent states close directly.
	for !s.State.IsTerminal() {
		next := protocol.SessionDraining
		if s.State == protocol.SessionDraining || s.State == protocol.SessionFenced {
			next = protocol.SessionClosed
		}
		if err := Transition(s.State, next); err != nil {
			// Defensive: force FAILED only when the table has no path.
			if terr := Transition(s.State, protocol.SessionFailed); terr != nil {
				return nil, terr
			}
			s.State = protocol.SessionFailed
			break
		}
		s.State = next
	}
	t := now
	s.ClosedAt = &t
	s.EndReason = reason
	s.WallSeconds = int64(now.Sub(s.CreatedAt).Seconds())
	m.mu.Lock()
	delete(m.last, sessionID)
	m.mu.Unlock()
	if err := m.Store.Save(s); err != nil {
		return nil, err
	}
	_ = m.Store.AppendEvent(s.SessionID, "closed", map[string]any{"reason": string(reason)})
	return s, nil
}

// FenceStaleSessions moves a node's sessions pinned to a superseded token
// to FENCED. Called on node re-enrollment: the old agent instance may still
// be alive as a zombie, and it must lose write authority immediately.
func (m *Manager) FenceStaleSessions(nodeID string, currentToken uint64) int {
	all, err := m.Store.List("")
	if err != nil {
		return 0
	}
	fenced := 0
	for _, s := range all {
		if s.NodeID != nodeID || s.FenceToken == currentToken || s.State.IsTerminal() {
			continue
		}
		if s.State == protocol.SessionFenced {
			continue
		}
		if err := Transition(s.State, protocol.SessionFenced); err != nil {
			continue
		}
		s.State = protocol.SessionFenced
		_ = m.Store.Save(s)
		_ = m.Store.AppendEvent(s.SessionID, "fenced", map[string]any{"token": currentToken})
		fenced++
	}
	m.mu.Lock()
	for _, s := range all {
		if s.NodeID == nodeID && s.FenceToken != currentToken {
			delete(m.last, s.SessionID)
		}
	}
	m.mu.Unlock()
	return fenced
}

// NoteSaveGen records a committed save generation on the session for
// history linkage. Called by the save manager after a successful commit;
// never fails the commit itself.
func (m *Manager) NoteSaveGen(sessionID string, gen uint64) {
	s, err := m.Store.Get(sessionID)
	if err != nil {
		return
	}
	for _, g := range s.SaveGens {
		if g == gen {
			return
		}
	}
	s.SaveGens = append(s.SaveGens, gen)
	_ = m.Store.Save(s)
}

// SessionsForNode returns sessions assigned to nodeID that still need a
// runner: NODE_ASSIGNED, or PREPARING without published stream info.
// Terminal, fenced, and already-streaming sessions are excluded — pickup
// is for fresh work, not for stealing live sessions.
func (m *Manager) SessionsForNode(nodeID string) ([]*protocol.Session, error) {
	all, err := m.Store.List("")
	if err != nil {
		return nil, err
	}
	var out []*protocol.Session
	for _, s := range all {
		if s.NodeID != nodeID || s.State.IsTerminal() || s.State == protocol.SessionFenced {
			continue
		}
		switch s.State {
		case protocol.SessionNodeAssigned:
			out = append(out, s)
		case protocol.SessionPreparing:
			if s.Stream.Host == "" {
				out = append(out, s)
			}
		}
	}
	return out, nil
}

// SetStream records the Moonlight connection info published by the node
// after backend readiness (provider/host/port/app). Fence-checked: only
// the live holder may advertise where clients should connect.
func (m *Manager) SetStream(sessionID, nodeID string, fenceToken uint64, cfg protocol.StreamConfig) (*protocol.Session, error) {
	s, err := m.Store.Get(sessionID)
	if err != nil {
		return nil, common.E(common.CodeNotFound, "unknown session", err)
	}
	if s.State.IsTerminal() || s.State == protocol.SessionFenced {
		return nil, common.E(common.CodeConflict, "session not streamable", nil)
	}
	if nodeID != s.NodeID || fenceToken != s.FenceToken {
		return nil, common.E(common.CodeFenced, "stale fence token: zombie stream refused", nil)
	}
	s.Stream = cfg
	if err := m.Store.Save(s); err != nil {
		return nil, err
	}
	return s, nil
}

// GameStats is the per-game rollup the client shell renders.
type GameStats struct {
	GameID         string  `json:"game_id"`
	TotalActiveSec int64   `json:"total_active_seconds"`
	Sessions       int     `json:"sessions"`
	LastPlayed     *string `json:"last_played,omitempty"`
}

// Playtime aggregates server-observed active seconds per game.
func (m *Manager) Playtime(userID string) ([]GameStats, error) {
	all, err := m.Store.List(userID)
	if err != nil {
		return nil, err
	}
	byGame := map[string]*GameStats{}
	for _, s := range all {
		st := byGame[s.GameID]
		if st == nil {
			st = &GameStats{GameID: s.GameID}
			byGame[s.GameID] = st
		}
		st.TotalActiveSec += s.ActiveSeconds
		st.Sessions++
		mark := s.CreatedAt.Format("2006-01-02")
		if s.ClosedAt != nil {
			mark = s.ClosedAt.Format("2006-01-02")
		}
		if st.LastPlayed == nil || mark > *st.LastPlayed {
			st.LastPlayed = &mark
		}
	}
	out := make([]GameStats, 0, len(byGame))
	for _, st := range byGame {
		out = append(out, *st)
	}
	return out, nil
}
