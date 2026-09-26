package postgres

import (
	"database/sql"
	"encoding/json"
	"time"

	controlSession "github.com/personal-game/personal-game/internal/control/session"
	"github.com/personal-game/personal-game/pkg/protocol"
)

// Compile-time guarantee: SessionStore satisfies the control-plane session
// contract, so swapping MemoryStore for PostgreSQL changes no callers.
var _ controlSession.Store = (*SessionStore)(nil)

// SessionStore is the PostgreSQL-backed session.Store.
type SessionStore struct {
	db *sql.DB
}

// NewSessionStore wraps an open *sql.DB (driver registered by the caller).
func NewSessionStore(db *sql.DB) *SessionStore { return &SessionStore{db: db} }

// Save implements session.Store (upsert by session id).
func (s *SessionStore) Save(sess *protocol.Session) error {
	stream, err := json.Marshal(sess.Stream)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO sessions
		(session_id, user_id, game_id, node_id, fence_token, state, created_at,
		 ready_at, closed_at, active_seconds, wall_seconds, prepare_seconds,
		 end_reason, stream)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (session_id) DO UPDATE SET
			node_id=EXCLUDED.node_id, fence_token=EXCLUDED.fence_token,
			state=EXCLUDED.state, ready_at=EXCLUDED.ready_at,
			closed_at=EXCLUDED.closed_at, active_seconds=EXCLUDED.active_seconds,
			wall_seconds=EXCLUDED.wall_seconds, prepare_seconds=EXCLUDED.prepare_seconds,
			end_reason=EXCLUDED.end_reason, stream=EXCLUDED.stream`,
		sess.SessionID, sess.UserID, sess.GameID, nullStr(sess.NodeID),
		sess.FenceToken, string(sess.State), sess.CreatedAt,
		nullTime(sess.ReadyAt), nullTime(sess.ClosedAt),
		sess.ActiveSeconds, sess.WallSeconds, sess.PrepareSeconds,
		nullStr(string(sess.EndReason)), string(stream))
	return err
}

// Get implements session.Store.
func (s *SessionStore) Get(id string) (*protocol.Session, error) {
	row := s.db.QueryRow(`SELECT session_id, user_id, game_id, node_id, fence_token,
		state, created_at, ready_at, closed_at, active_seconds, wall_seconds,
		prepare_seconds, end_reason, stream
		FROM sessions WHERE session_id=$1`, id)
	return scanSession(row)
}

// List implements session.Store (empty userID lists all; newest first).
func (s *SessionStore) List(userID string) ([]*protocol.Session, error) {
	var rows *sql.Rows
	var err error
	if userID == "" {
		rows, err = s.db.Query(`SELECT session_id, user_id, game_id, node_id, fence_token,
			state, created_at, ready_at, closed_at, active_seconds, wall_seconds,
			prepare_seconds, end_reason, stream
			FROM sessions ORDER BY created_at DESC`)
	} else {
		rows, err = s.db.Query(`SELECT session_id, user_id, game_id, node_id, fence_token,
			state, created_at, ready_at, closed_at, active_seconds, wall_seconds,
			prepare_seconds, end_reason, stream
			FROM sessions WHERE user_id=$1 ORDER BY created_at DESC`, userID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*protocol.Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// AppendEvent implements session.Store.
func (s *SessionStore) AppendEvent(sessionID, event string, detail map[string]any) error {
	d, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO session_events (session_id, event, detail)
		VALUES ($1,$2,$3)`, sessionID, event, string(d))
	return err
}

func scanSession(r rowScanner) (*protocol.Session, error) {
	var s protocol.Session
	var nodeID, endReason, streamRaw sql.NullString
	var readyAt, closedAt sql.NullTime
	var state string
	if err := r.Scan(&s.SessionID, &s.UserID, &s.GameID, &nodeID, &s.FenceToken,
		&state, &s.CreatedAt, &readyAt, &closedAt,
		&s.ActiveSeconds, &s.WallSeconds, &s.PrepareSeconds,
		&endReason, &streamRaw); err != nil {
		return nil, err
	}
	s.SchemaVersion = protocol.SessionSchemaVersion
	s.NodeID = nodeID.String
	s.State = protocol.SessionState(state)
	if readyAt.Valid {
		t := readyAt.Time
		s.ReadyAt = &t
	}
	if closedAt.Valid {
		t := closedAt.Time
		s.ClosedAt = &t
	}
	s.EndReason = protocol.SessionEndReason(endReason.String)
	if streamRaw.Valid && streamRaw.String != "" {
		if err := json.Unmarshal([]byte(streamRaw.String), &s.Stream); err != nil {
			return nil, err
		}
	}
	// SaveGens is intentionally not rehydrated here: it is a live-run
	// linkage list (rebuilt by new commits via NoteSaveGen), while durable
	// history lives in save_snapshots keyed by session_id. Playtime and
	// pointers are unaffected by restarts.
	return &s, nil
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}
