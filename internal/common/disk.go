package common

import "github.com/personal-game/personal-game/pkg/protocol"

// DiskPlan is the result of a pre-acquisition capacity check.
type DiskPlan struct {
	RequiredBytes  uint64
	FreeBytes      uint64
	EvictableBytes uint64
	Fits           bool // fits without eviction
	FitsAfterEvict bool // fits if eligible cache is evicted
}

// PlanDisk computes whether a game footprint fits.
// evictableBytes = bytes of installed-game cache eligible for eviction
// (never includes saves, downloads-in-progress of the active session, or
// the game currently being launched).
func PlanDisk(fp protocol.Footprint, freeBytes, evictableBytes uint64) DiskPlan {
	req := fp.RequiredBytes()
	return DiskPlan{
		RequiredBytes:  req,
		FreeBytes:      freeBytes,
		EvictableBytes: evictableBytes,
		Fits:           freeBytes >= req,
		FitsAfterEvict: freeBytes+evictableBytes >= req,
	}
}
