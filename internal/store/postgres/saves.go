// Package postgres documents the persistence boundary and hosts the SQL
// save store. PostgreSQL is authoritative for pointers, generations,
// metadata, ordering, and session relationships.
//
// Connection requires a pg driver (e.g. github.com/lib/pq or pgx) registered
// under DriverName; compose pins postgres:16 for local runs. The store below
// uses only database/sql so the module stays dependency-free.
package postgres

import (
	"database/sql"

	"github.com/personal-game/personal-game/pkg/protocol"
)

// SaveStore is the PostgreSQL-backed saves.Store. All mutating paths run in
// transactions with row locks so concurrent begins serialize generations and
// pointer moves stay monotonic even under node races.
type SaveStore struct {
	db *sql.DB
}

// NewSaveStore wraps an open *sql.DB (driver registered by the caller).
func NewSaveStore(db *sql.DB) *SaveStore { return &SaveStore{db: db} }

// IssuePending implements saves.Store: generation = max+1 under lock.
func (s *SaveStore) IssuePending(userID, gameID, nodeID, sessionID string, parent *uint64) (*protocol.SaveSnapshot, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var max uint64
	err = tx.QueryRow(`SELECT COALESCE(MAX(generation),0) FROM save_snapshots
		WHERE user_id=$1 AND game_id=$2 FOR UPDATE`, userID, gameID).Scan(&max)
	if err != nil {
		return nil, err
	}
	gen := max + 1
	var parentArg any
	if parent != nil {
		parentArg = *parent
	}
	var created string
	_ = created
	_, err = tx.Exec(`INSERT INTO save_snapshots
		(user_id, game_id, generation, sha256, size_bytes, file_count, node_id, session_id, state, parent_generation)
		VALUES ($1,$2,$3,'',0,0,$4,$5,'PENDING',$6)`,
		userID, gameID, gen, nodeID, sessionID, parentArg)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &protocol.SaveSnapshot{
		SchemaVersion: protocol.SaveSchemaVersion,
		UserID:        userID, GameID: gameID, Generation: gen,
		NodeID: nodeID, SessionID: sessionID,
		State: protocol.SavePending, ParentGeneration: parent,
	}, nil
}

// Finalize implements saves.Store: PENDING -> VALID/CHECKPOINT with metadata.
func (s *SaveStore) Finalize(userID, gameID string, gen uint64, to protocol.SaveState, sha string, size uint64, files int) (*protocol.SaveSnapshot, error) {
	if to != protocol.SaveValid && to != protocol.SaveCheckpoint {
		return nil, errBadState(to)
	}
	res, err := s.db.Exec(`UPDATE save_snapshots SET state=$1, sha256=$2, size_bytes=$3, file_count=$4
		WHERE user_id=$5 AND game_id=$6 AND generation=$7 AND state='PENDING'`,
		string(to), sha, size, files, userID, gameID, gen)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, errNotPending(gen)
	}
	return s.Get(userID, gameID, gen)
}

// Mark implements saves.Store.
func (s *SaveStore) Mark(userID, gameID string, gen uint64, to protocol.SaveState) error {
	_, err := s.db.Exec(`UPDATE save_snapshots SET state=$1
		WHERE user_id=$2 AND game_id=$3 AND generation=$4`,
		string(to), userID, gameID, gen)
	return err
}

// Get implements saves.Store.
func (s *SaveStore) Get(userID, gameID string, gen uint64) (*protocol.SaveSnapshot, error) {
	row := s.db.QueryRow(`SELECT user_id, game_id, generation, sha256, size_bytes,
		file_count, node_id, session_id, created_at, state, parent_generation
		FROM save_snapshots WHERE user_id=$1 AND game_id=$2 AND generation=$3`,
		userID, gameID, gen)
	return scanSnapshot(row)
}

// Pointer implements saves.Store (0 when no pointer row exists yet).
func (s *SaveStore) Pointer(userID, gameID string) (uint64, error) {
	var gen uint64
	err := s.db.QueryRow(`SELECT latest_generation FROM save_pointers
		WHERE user_id=$1 AND game_id=$2`, userID, gameID).Scan(&gen)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return gen, err
}

// Advance implements saves.Store: strictly forward, upserting the pointer.
func (s *SaveStore) Advance(userID, gameID string, gen uint64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var cur uint64
	err = tx.QueryRow(`SELECT latest_generation FROM save_pointers
		WHERE user_id=$1 AND game_id=$2 FOR UPDATE`, userID, gameID).Scan(&cur)
	switch {
	case err == sql.ErrNoRows:
		_, err = tx.Exec(`INSERT INTO save_pointers (user_id, game_id, latest_generation)
			VALUES ($1,$2,$3)`, userID, gameID, gen)
	case err == nil && gen > cur:
		_, err = tx.Exec(`UPDATE save_pointers SET latest_generation=$1, updated_at=now()
			WHERE user_id=$2 AND game_id=$3`, gen, userID, gameID)
	case err == nil:
		return errBackward(gen)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Latest implements saves.Store.
func (s *SaveStore) Latest(userID, gameID string) (*protocol.SaveSnapshot, error) {
	gen, err := s.Pointer(userID, gameID)
	if err != nil {
		return nil, err
	}
	if gen == 0 {
		return nil, errNone()
	}
	return s.Get(userID, gameID, gen)
}

// History implements saves.Store (newest first, most recent `limit`).
func (s *SaveStore) History(userID, gameID string, limit int) ([]*protocol.SaveSnapshot, error) {
	q := `SELECT user_id, game_id, generation, sha256, size_bytes,
		file_count, node_id, session_id, created_at, state, parent_generation
		FROM save_snapshots WHERE user_id=$1 AND game_id=$2 ORDER BY generation DESC`
	var rows *sql.Rows
	var err error
	if limit > 0 {
		q += ` LIMIT $3`
		rows, err = s.db.Query(q, userID, gameID, limit)
	} else {
		rows, err = s.db.Query(q, userID, gameID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*protocol.SaveSnapshot
	for rows.Next() {
		snap, err := scanSnapshot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, snap)
	}
	return out, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSnapshot(r rowScanner) (*protocol.SaveSnapshot, error) {
	var s protocol.SaveSnapshot
	var nodeID, sessionID sql.NullString
	var created sql.NullTime
	var parent sql.NullInt64
	var state string
	if err := r.Scan(&s.UserID, &s.GameID, &s.Generation, &s.SHA256, &s.SizeBytes,
		&s.FileCount, &nodeID, &sessionID, &created, &state, &parent); err != nil {
		return nil, err
	}
	s.SchemaVersion = protocol.SaveSchemaVersion
	s.NodeID = nodeID.String
	s.SessionID = sessionID.String
	if created.Valid {
		s.CreatedAt = created.Time
	}
	s.State = protocol.SaveState(state)
	if parent.Valid {
		p := uint64(parent.Int64)
		s.ParentGeneration = &p
	}
	// No Validate() call here: PENDING/ORPHANED rows legitimately lack blob
	// metadata until (or when never) finalized. Commit/Finalize boundaries
	// enforce shape instead.
	return &s, nil
}
