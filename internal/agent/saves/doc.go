// Package saves implements agent-side save handling (Stage 3) behind the
// existing fixed command model: RESTORE maps to Restore, SNAPSHOT maps to
// Snapshot. All save paths resolve through the manifest's save provider and
// overrides; there are no arbitrary file-copy commands.
//
// Rules enforced here:
//   - never write a partial download into a live save directory
//     (temp -> verify -> stage -> atomic swap, with rollback)
//   - never snapshot files while they are being written (quiescence wait)
//   - every path validated against approved roots (no traversal)
package saves

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/personal-game/personal-game/internal/common"
	"github.com/personal-game/personal-game/pkg/protocol"
)

// ResolveDirs returns the live save directories for a game: manifest
// overrides first (project-specific knowledge, e.g. drawn from Ludusavi's
// database by the operator), environment-expanded and validated. The
// "ludusavi" provider means paths follow Ludusavi conventions; Ludusavi
// itself remains the knowledge source, not a runtime dependency.
func ResolveDirs(m protocol.GameManifest) ([]string, error) {
	var raw []string
	for _, o := range m.Saves.Overrides {
		if o.Platform == "" || o.Platform == runtime.GOOS || o.Platform == "any" {
			raw = append(raw, o.Paths...)
		}
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("saves: no save locations for %s on %s (add manifest overrides)",
			m.GameID, runtime.GOOS)
	}
	roots, err := SaveRoots()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range raw {
		expanded := ExpandPath(p)
		clean, verr := common.ValidatePath(roots, expanded)
		if verr != nil {
			return nil, fmt.Errorf("saves: save path escapes approved roots: %w", verr)
		}
		out = append(out, clean)
	}
	return out, nil
}

// SaveRoots returns the approved roots save data may live under:
// the user's home/profile tree (plus the game install for portable saves).
// Operators may declare additional roots via PG_SAVE_ROOTS (os.PathListSeparator
// separated) for environments where save/game data legitimately lives outside
// the home volume (e.g. a node work area on another drive). Extra roots
// widen the approved set explicitly; they never bypass validation.
func SaveRoots() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil, fmt.Errorf("saves: cannot determine user home: %w", err)
	}
	roots := []string{home}
	if extra := os.Getenv("PG_SAVE_ROOTS"); extra != "" {
		for _, r := range filepath.SplitList(extra) {
			if r = strings.TrimSpace(r); r != "" {
				roots = append(roots, r)
			}
		}
	}
	return roots, nil
}

// ExpandPath expands ~, $VAR/${VAR}, and %VAR% (Windows) prefixes.
func ExpandPath(p string) string {
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	p = os.ExpandEnv(p)
	if runtime.GOOS == "windows" {
		p = expandWinEnv(p)
	}
	return p
}

// expandWinEnv expands %NAME% variables without touching lone % signs.
func expandWinEnv(p string) string {
	var b strings.Builder
	i := 0
	for i < len(p) {
		if p[i] != '%' {
			b.WriteByte(p[i])
			i++
			continue
		}
		j := strings.IndexByte(p[i+1:], '%')
		if j < 0 {
			b.WriteByte(p[i])
			i++
			continue
		}
		name := p[i+1 : i+1+j]
		if v, ok := os.LookupEnv(name); ok {
			b.WriteString(v)
		} else {
			b.WriteString(p[i : i+1+j+1])
		}
		i += 1 + j + 1
	}
	return b.String()
}
