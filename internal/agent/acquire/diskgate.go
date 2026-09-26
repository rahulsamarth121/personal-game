package acquire

import (
	"fmt"

	"github.com/personal-game/personal-game/pkg/protocol"
)

// DiskReport is the user-facing capacity verdict. Refusing early avoids
// half-downloaded giant archives when failure is already predictable.
type DiskReport struct {
	RequiredBytes  uint64
	FreeBytes      uint64
	EvictableBytes uint64
	Fits           bool
	FitsAfterEvict bool
	MissingBytes   uint64
	Message        string
}

// CheckDisk plans the footprint against free + evictable cache bytes.
// evictableBytes must exclude saves, the active game, and in-flight stages.
func CheckDisk(fp protocol.Footprint, freeBytes, evictableBytes uint64) DiskReport {
	return checkDisk(fp.RequiredBytes(), freeBytes, evictableBytes)
}

// CheckDiskFor is the package-type-aware gate: multipart-installer budgets
// ISO/installer overhead that prebuilt and direct modes never incur.
func CheckDiskFor(m protocol.GameManifest, freeBytes, evictableBytes uint64) DiskReport {
	return checkDisk(m.Footprint.RequiredFor(EffectiveType(m)), freeBytes, evictableBytes)
}

func checkDisk(required, freeBytes, evictableBytes uint64) DiskReport {
	r := DiskReport{
		RequiredBytes: required, FreeBytes: freeBytes,
		EvictableBytes: evictableBytes,
		Fits:           freeBytes >= required,
		FitsAfterEvict: freeBytes+evictableBytes >= required,
	}
	switch {
	case r.Fits:
		r.Message = fmt.Sprintf("disk OK: required %s, available %s",
			human(required), human(freeBytes))
	case r.FitsAfterEvict:
		r.Message = fmt.Sprintf("disk OK after evicting %s of game cache: required %s, available %s",
			human(required-freeBytes), human(required), human(freeBytes))
	default:
		r.MissingBytes = required - freeBytes - evictableBytes
		r.Message = fmt.Sprintf("insufficient disk: required %s, available %s, recoverable cache %s, still missing %s",
			human(required), human(freeBytes),
			human(evictableBytes), human(r.MissingBytes))
	}
	return r
}

func human(n uint64) string {
	const gb = 1024 * 1024 * 1024
	const mb = 1024 * 1024
	switch {
	case n >= 10*gb:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(gb))
	case n >= 10*mb:
		return fmt.Sprintf("%.0f MB", float64(n)/float64(mb))
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}
