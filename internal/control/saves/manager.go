package saves

import (
	"time"

	"github.com/personal-game/personal-game/internal/common"
	"github.com/personal-game/personal-game/internal/control/session"
	"github.com/personal-game/personal-game/internal/store/object"
	"github.com/personal-game/personal-game/pkg/protocol"
)

// Manager implements the save upload/commit/pointer flow with fencing:
// the control plane issues generations, the node uploads immutable blobs,
// and only a live-fenced holder can advance the latest pointer.
type Manager struct {
	Saves    Store
	Objects  object.Store
	Sessions *session.Manager
	Now      func() time.Time

	UploadTTL   time.Duration
	DownloadTTL time.Duration

	// OnCommitted, when set, records the generation on the session
	// (history/playtime linkage). Failures here never fail the commit.
	OnCommitted func(sessionID string, gen uint64)
}

// NewManager builds a Manager with sane TTL defaults.
func NewManager(saves Store, objects object.Store, sessions *session.Manager) *Manager {
	return &Manager{
		Saves: sSaves(saves), Objects: objects, Sessions: sessions,
		Now:         time.Now,
		UploadTTL:   15 * time.Minute,
		DownloadTTL: 15 * time.Minute,
	}
}

func sSaves(s Store) Store { return s }

// BeginGrant hands the node an immutable generation plus a scoped upload URL.
type BeginGrant struct {
	Generation uint64 `json:"generation"`
	UploadURL  string `json:"upload_url"`
	Key        string `json:"key"`
}

// liveSession loads the session and enforces holder identity + liveness.
// Zombie (stale token), foreign node, and terminal sessions are refused.
func (m *Manager) liveSession(sessionID, nodeID string, token uint64) (*protocol.Session, error) {
	s, err := m.Sessions.Store.Get(sessionID)
	if err != nil {
		return nil, common.E(common.CodeNotFound, "unknown session", err)
	}
	if s.State.IsTerminal() {
		return nil, common.E(common.CodeConflict, "session already closed", nil)
	}
	if s.State == protocol.SessionFenced {
		return nil, common.E(common.CodeFenced, "session fenced: no save operations", nil)
	}
	if nodeID != s.NodeID || token != s.FenceToken {
		return nil, common.E(common.CodeFenced, "stale fence token: zombie save refused", nil)
	}
	return s, nil
}

// Begin validates the lease/fence, issues generation max+1 (PENDING), and
// returns a scoped upload URL for the immutable blob key.
func (m *Manager) Begin(sessionID, nodeID string, token uint64) (BeginGrant, error) {
	s, err := m.liveSession(sessionID, nodeID, token)
	if err != nil {
		return BeginGrant{}, err
	}
	parent, _ := m.Saves.Pointer(s.UserID, s.GameID)
	var parentPtr *uint64
	if parent > 0 {
		parentPtr = &parent
	}
	snap, err := m.Saves.IssuePending(s.UserID, s.GameID, nodeID, sessionID, parentPtr)
	if err != nil {
		return BeginGrant{}, common.E(common.CodeInternal, "could not issue generation", err)
	}
	key := snap.ObjectKey()
	upURL, err := m.Objects.PresignUpload(ctxBG(), key, "", m.UploadTTL)
	if err != nil {
		_ = m.Saves.Mark(s.UserID, s.GameID, snap.Generation, protocol.SaveCorrupt)
		return BeginGrant{}, common.E(common.CodeStorage, "could not authorize upload", err)
	}
	return BeginGrant{Generation: snap.Generation, UploadURL: upURL, Key: key}, nil
}

// CommitParams carries the node's post-upload claim.
type CommitParams struct {
	SessionID  string
	NodeID     string
	Token      uint64
	Generation uint64
	SHA256     string
	SizeBytes  uint64
	FileCount  int
	Checkpoint bool // mid-session checkpoint vs post-exit authoritative save
}

// Commit re-validates the fence, verifies ordering (strictly newer than
// latest), marks VALID/CHECKPOINT, and advances the pointer. On fence
// failure the blob is marked ORPHANED and latest is untouched: a zombie
// can never destroy newer progress.
func (m *Manager) Commit(p CommitParams) (*protocol.SaveSnapshot, error) {
	s, err := m.liveSession(p.SessionID, p.NodeID, p.Token)
	if err != nil {
		// Best effort: orphan the generation so it is never referenced.
		_ = m.orphanBySession(p.SessionID, p.Generation)
		return nil, err
	}
	latest, _ := m.Saves.Pointer(s.UserID, s.GameID)
	if len(p.SHA256) != 64 {
		_ = m.Saves.Mark(s.UserID, s.GameID, p.Generation, protocol.SaveCorrupt)
		return nil, common.E(common.CodeInvalidInput, "commit requires 64-char sha256", nil)
	}
	// Duplicate delivery of an already-committed generation with identical
	// bytes is an idempotent retry (success); anything else proceeds to
	// ordering validation below.
	if dup, derr := m.Saves.Get(s.UserID, s.GameID, p.Generation); derr == nil &&
		(dup.State == protocol.SaveValid || dup.State == protocol.SaveCheckpoint) &&
		dup.SHA256 == p.SHA256 && dup.SizeBytes == p.SizeBytes {
		if m.OnCommitted != nil {
			m.OnCommitted(p.SessionID, p.Generation)
		}
		return dup, nil
	}
	if err := session.DecideCommit(session.CommitSaveParams{
		CurrentToken: s.FenceToken, HolderToken: p.Token,
		LatestGen: latest, NewGen: p.Generation,
	}); err != nil {
		_ = m.Saves.Mark(s.UserID, s.GameID, p.Generation, protocol.SaveOrphaned)
		return nil, err
	}
	to := protocol.SaveValid
	if p.Checkpoint {
		to = protocol.SaveCheckpoint
	}
	snap, err := m.Saves.Finalize(s.UserID, s.GameID, p.Generation, to, p.SHA256, p.SizeBytes, p.FileCount)
	if err != nil {
		return nil, common.E(common.CodeConflict, "could not finalize save", err)
	}
	if err := m.Saves.Advance(s.UserID, s.GameID, p.Generation); err != nil {
		return nil, common.E(common.CodeConflict, "could not advance pointer", err)
	}
	snap.CreatedAt = m.Now()
	if m.OnCommitted != nil {
		m.OnCommitted(p.SessionID, p.Generation)
	}
	return snap, nil
}

// orphanBySession marks a generation ORPHANED via session lookup (used when
// the session itself failed validation and user/game are still resolvable).
func (m *Manager) orphanBySession(sessionID string, gen uint64) error {
	s, err := m.Sessions.Store.Get(sessionID)
	if err != nil {
		return err
	}
	return m.Saves.Mark(s.UserID, s.GameID, gen, protocol.SaveOrphaned)
}

// LatestGrant returns the latest snapshot plus a scoped download URL.
func (m *Manager) LatestGrant(userID, gameID string) (*protocol.SaveSnapshot, string, error) {
	snap, err := m.Saves.Latest(userID, gameID)
	if err != nil {
		return nil, "", common.E(common.CodeNotFound, "no saves yet", err)
	}
	dlURL, err := m.Objects.PresignDownload(ctxBG(), snap.ObjectKey(), m.DownloadTTL)
	if err != nil {
		return nil, "", common.E(common.CodeStorage, "could not authorize download", err)
	}
	return snap, dlURL, nil
}
