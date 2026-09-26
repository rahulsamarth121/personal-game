// Package catalog holds the game-manifest registry interface.
// PostgreSQL-backed implementation lands in Stage 2/5.
package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/personal-game/personal-game/pkg/protocol"
)

// Registry stores validated manifests in memory (foundation default).
// The control plane will swap this for the postgres implementation
// without changing callers.
type Registry struct {
	mu    sync.RWMutex
	games map[string]protocol.GameManifest
}

// New returns an empty registry.
func New() *Registry { return &Registry{games: map[string]protocol.GameManifest{}} }

// Put validates and stores a manifest.
func (r *Registry) Put(m protocol.GameManifest) error {
	if err := m.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.games[m.GameID] = m
	return nil
}

// Get returns a manifest by id.
func (r *Registry) Get(id string) (protocol.GameManifest, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.games[id]
	if !ok {
		return protocol.GameManifest{}, errors.New("catalog: game not found")
	}
	return m, nil
}

// List returns all manifests.
func (r *Registry) List() []protocol.GameManifest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]protocol.GameManifest, 0, len(r.games))
	for _, m := range r.games {
		out = append(out, m)
	}
	return out
}

// SaveToFile writes the manifest for id into dir as <id>.json (temp file +
// rename) so games added via POST /v1/games (GUI/CLI onboarding) survive a
// control-plane restart, matching the seeded `games/manifests` directory the
// registry already loads at boot. It refuses anything but a plain,
// filename-safe id so a hostile id can never escape the seed directory.
func (r *Registry) SaveToFile(id, dir string) error {
	m, err := r.Get(id)
	if err != nil {
		return err
	}
	if err := validManifestID(id); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("catalog: create %s: %w", dir, err)
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("catalog: encode %s: %w", id, err)
	}
	dest := filepath.Join(dir, id+".json")
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("catalog: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("catalog: persist %s: %w", dest, err)
	}
	return nil
}

// validManifestID allows only [A-Za-z0-9._-] (1..64 chars, no leading dot)
// so the id is safe as a bare filename under the seed directory.
func validManifestID(id string) error {
	if id == "" || len(id) > 64 {
		return errors.New("catalog: invalid game id length")
	}
	if id[0] == '.' {
		return errors.New("catalog: game id must not start with a dot")
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
		default:
			return fmt.Errorf("catalog: game id %q contains unsupported characters", id)
		}
	}
	return nil
}

// LoadDir seeds the registry from every *.json manifest in dir, validated.
// Files ending in .example.json are skipped (documentation, not catalog).
// Returns the loaded ids; any invalid file aborts the whole seed loudly
// rather than starting with a half-loaded catalog.
func (r *Registry) LoadDir(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("catalog: read seed dir %s: %w", dir, err)
	}
	var ids []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".json" || strings.HasSuffix(name, ".example.json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("catalog: read %s: %w", name, err)
		}
		var m protocol.GameManifest
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("catalog: %s is not valid JSON: %w", name, err)
		}
		if err := r.Put(m); err != nil {
			return nil, fmt.Errorf("catalog: %s invalid: %w", name, err)
		}
		ids = append(ids, m.GameID)
	}
	return ids, nil
}
