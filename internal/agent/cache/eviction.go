package cache

import "sort"

// EvictionPlan picks installed games to remove (oldest last-used first)
// until needBytes is covered. Protected entries and the active game are
// never selected. Returns the ordered victim list and whether it covers
// the need; the caller performs deletion and rechecks disk afterwards.
func EvictionPlan(entries []Entry, activeGameID string, needBytes uint64) ([]Entry, bool) {
	var candidates []Entry
	for _, e := range entries {
		if e.GameID == activeGameID || e.Protected {
			continue
		}
		candidates = append(candidates, e)
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].LastUsedUnix < candidates[j].LastUsedUnix
	})
	var victims []Entry
	var freed uint64
	for _, c := range candidates {
		victims = append(victims, c)
		freed += c.Bytes
		if freed >= needBytes {
			return victims, true
		}
	}
	return victims, false
}
