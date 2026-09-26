package capability

import (
	"os"
	"strings"

	"github.com/personal-game/personal-game/pkg/protocol"
)

// diskCap reports capacity of the working directory volume.
// Cross-platform without syscalls: best-effort via env override, else zero.
// Full statfs-based reporting is a Stage 1 enhancement; zero means "unknown"
// and the scheduler treats it as constrained (safe default).
func diskCap() protocol.DiskCap {
	// PG_DISK_TOTAL_BYTES / PG_DISK_FREE_BYTES allow tests and containers
	// to inject values without native syscalls.
	total := envUint("PG_DISK_TOTAL_BYTES")
	free := envUint("PG_DISK_FREE_BYTES")
	var fstype string
	if runtime_isWindows() {
		fstype = "ntfs"
	}
	_ = fstype
	return protocol.DiskCap{TotalBytes: total, FreeBytes: free}
}

func envUint(key string) uint64 {
	v := os.Getenv(key)
	if v == "" {
		return 0
	}
	var n uint64
	for _, ch := range strings.TrimSpace(v) {
		if ch < '0' || ch > '9' {
			return 0
		}
		n = n*10 + uint64(ch-'0')
	}
	return n
}
