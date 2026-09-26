package acquire

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/personal-game/personal-game/pkg/protocol"
)

// LoadManifest reads and validates a game manifest from disk.
func LoadManifest(path string) (protocol.GameManifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return protocol.GameManifest{}, fmt.Errorf("acquire: read manifest: %w", err)
	}
	var m protocol.GameManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return protocol.GameManifest{}, fmt.Errorf("acquire: parse manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return protocol.GameManifest{}, err
	}
	return m, nil
}

// OrderedSources returns the manifest's sources sorted by explicit part
// number. Host filenames may be imperfect; part order is authoritative.
func OrderedSources(m protocol.GameManifest) []protocol.AcquisitionSource {
	out := append([]protocol.AcquisitionSource(nil), m.Acquisition.Sources...)
	sort.Slice(out, func(i, j int) bool { return out[i].Part < out[j].Part })
	return out
}

// Dirs separates download, extraction, installed-game, and state tiers.
// Only GamesDir is evictable cache; saves live elsewhere entirely.
type Dirs struct {
	Download string // /cache/downloads
	Extract  string // /cache/extract
	Games    string // /games (only evictable tier)
	State    string // /state (pipeline JSON, never user saves)
}

// Ensure creates all tiers.
func (d Dirs) Ensure() error {
	for _, dir := range []string{d.Download, d.Extract, d.Games, d.State} {
		if dir == "" {
			return fmt.Errorf("acquire: empty tier directory")
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("acquire: mkdir %s: %w", dir, err)
		}
	}
	return nil
}

// Pipeline tracks one game's preparation. Persisted as JSON so restarts
// resume instead of re-downloading verified stages.
type Pipeline struct {
	Manifest protocol.GameManifest  `json:"manifest"`
	State    protocol.PipelineState `json:"state"`
	Error    string                 `json:"error,omitempty"`
	Dirs     Dirs                   `json:"-"`
}

// StateFile returns the persisted pipeline path for a game.
func (d Dirs) StateFile(gameID string) string {
	return filepath.Join(d.State, gameID+".pipeline.json")
}

// LoadPipeline resumes persisted state, or starts at PLANNED.
func LoadPipeline(d Dirs, m protocol.GameManifest) (*Pipeline, error) {
	p := &Pipeline{Manifest: m, State: protocol.PipePlanned, Dirs: d}
	raw, err := os.ReadFile(d.StateFile(m.GameID))
	if err != nil {
		if os.IsNotExist(err) {
			return p, nil
		}
		return nil, err
	}
	var saved struct {
		State protocol.PipelineState `json:"state"`
		Error string                 `json:"error,omitempty"`
	}
	if err := json.Unmarshal(raw, &saved); err != nil {
		return nil, fmt.Errorf("acquire: corrupt pipeline state: %w", err)
	}
	p.State = saved.State
	p.Error = saved.Error
	return p, nil
}

// Save persists the pipeline atomically (temp + rename).
func (p *Pipeline) Save() error {
	raw, _ := json.MarshalIndent(struct {
		State protocol.PipelineState `json:"state"`
		Error string                 `json:"error,omitempty"`
	}{p.State, p.Error}, "", "  ")
	tmp := p.Dirs.StateFile(p.Manifest.GameID) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p.Dirs.StateFile(p.Manifest.GameID))
}

// Advance enforces the transition table and persists.
func (p *Pipeline) Advance(to protocol.PipelineState) error {
	if !p.State.CanTransition(to) {
		return fmt.Errorf("acquire: illegal pipeline transition %s -> %s", p.State, to)
	}
	p.State = to
	if to != protocol.PipeFailed {
		p.Error = ""
	}
	return p.Save()
}

// Fail records a structured error and moves to FAILED from any state that
// allows it (all non-terminal states do).
func (p *Pipeline) Fail(reason string, cause error) error {
	msg := reason
	if cause != nil {
		msg += ": " + cause.Error()
	}
	p.Error = msg
	if p.State == protocol.PipeReady || p.State == protocol.PipeFailed {
		return p.Save()
	}
	p.State = protocol.PipeFailed
	return p.Save()
}
