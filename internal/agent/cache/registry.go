package cache

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/personal-game/personal-game/internal/common"
)

// Record tracks one installed game for usage and eviction decisions.
// The installed files under /games are authoritative; this registry is an
// index rebuilt from scans when missing (never the source of truth).
type Record struct {
	GameID       string `json:"game_id"`
	Version      string `json:"version"`
	Bytes        uint64 `json:"bytes"`
	LastUsedUnix int64  `json:"last_used_unix"`
	Protected    bool   `json:"protected"`
}

// Registry persists records as JSON beside the state dir (atomic writes).
// Saves, downloads, and staging are never recorded here.
type Registry struct {
	mu   sync.Mutex
	path string
	recs map[string]Record
}

// Open loads the registry (missing file starts empty).
func Open(path string) (*Registry, error) {
	r := &Registry{path: path, recs: map[string]Record{}}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return r, nil
		}
		return nil, err
	}
	var recs []Record
	if err := json.Unmarshal(raw, &recs); err != nil {
		return nil, fmt.Errorf("cache: corrupt registry %s: %w", path, err)
	}
	for _, rec := range recs {
		r.recs[rec.GameID] = rec
	}
	return r, nil
}

func (r *Registry) saveLocked() error {
	var recs []Record
	for _, rec := range r.recs {
		recs = append(recs, rec)
	}
	raw, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}

// MarkInstalled records (or refreshes) a validated game.
func (r *Registry) MarkInstalled(gameID, version string, bytes uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.recs[gameID]
	rec.GameID = gameID
	rec.Version = version
	rec.Bytes = bytes
	rec.LastUsedUnix = time.Now().Unix()
	r.recs[gameID] = rec
	return r.saveLocked()
}

// Touch refreshes last-used (call on session start).
func (r *Registry) Touch(gameID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.recs[gameID]
	if !ok {
		return nil
	}
	rec.LastUsedUnix = time.Now().Unix()
	r.recs[gameID] = rec
	return r.saveLocked()
}

// Protect pins/unpins a game against eviction (active sessions protected).
func (r *Registry) Protect(gameID string, on bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.recs[gameID]
	if !ok {
		return nil
	}
	rec.Protected = on
	r.recs[gameID] = rec
	return r.saveLocked()
}

// Remove drops a record (after the files are actually deleted).
func (r *Registry) Remove(gameID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.recs, gameID)
	return r.saveLocked()
}

func (r *Registry) entriesLocked() []Entry {
	var out []Entry
	for _, rec := range r.recs {
		out = append(out, Entry{
			GameID: rec.GameID, Version: rec.Version, Bytes: rec.Bytes,
			Protected: rec.Protected, LastUsedUnix: rec.LastUsedUnix,
		})
	}
	return out
}

// Evictable sums reclaimable bytes (never the active game, never protected,
// never saves — saves are not records at all).
func (r *Registry) Evictable(activeGameID string) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return EvictableBytes(r.entriesLocked(), activeGameID)
}

// Victims selects LRU victims covering needBytes (oldest first), or reports
// insufficient cover. The caller deletes files, then calls Remove per game.
func (r *Registry) Victims(activeGameID string, needBytes uint64) ([]Entry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return EvictionPlan(r.entriesLocked(), activeGameID, needBytes)
}

// EvictForNeed deletes victim game directories under gamesDir until freed
// covers needBytes (or returns false when even full eligible eviction falls
// short). Only recorded, non-protected, non-active games are touched —
// saves, downloads, and staging are never records and can never match.
// Records are removed only after their files are actually deleted.
func EvictForNeed(gamesDir string, r *Registry, activeGameID string, needBytes uint64) (freed uint64, ok bool) {
	victims, ok := r.Victims(activeGameID, needBytes)
	if !ok {
		return 0, false
	}
	for _, v := range victims {
		clean, err := common.ValidatePath([]string{gamesDir}, filepath.Join(gamesDir, v.GameID))
		if err != nil {
			continue
		}
		freed += dirSize(clean) // measured, not recorded: records may be stale
		if err := os.RemoveAll(clean); err != nil {
			continue
		}
		_ = r.Remove(v.GameID)
	}
	return freed, freed >= needBytes
}

// dirSize walks a tree summing file sizes (missing dir counts 0).
func dirSize(root string) uint64 {
	var n uint64
	_ = filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			n += uint64(info.Size())
		}
		return nil
	})
	return n
}
