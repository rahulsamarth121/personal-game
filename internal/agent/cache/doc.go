// Package cache manages installed-game cache (Stage 6).
// Only /games is evictable; saves and active sessions are protected.
package cache

// Entry tracks one installed game for eviction decisions.
type Entry struct {
	GameID       string
	Version      string
	Bytes        uint64
	Protected    bool
	LastUsedUnix int64
}

// EvictableBytes sums non-protected entries excluding the active game.
func EvictableBytes(entries []Entry, activeGameID string) uint64 {
	var n uint64
	for _, e := range entries {
		if e.GameID == activeGameID || e.Protected {
			continue
		}
		n += e.Bytes
	}
	return n
}
