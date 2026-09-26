package capability

import (
	"os"
	"runtime"
	"strings"
)

// totalRAMMB returns usable RAM in MiB (best effort, stdlib only).
func totalRAMMB() uint64 {
	switch runtime.GOOS {
	case "linux":
		b, err := os.ReadFile("/proc/meminfo")
		if err != nil {
			return 0
		}
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "MemTotal:") {
				f := strings.Fields(line) // MemTotal: <kB> kB
				if len(f) >= 2 {
					var kb uint64
					for _, ch := range f[1] {
						if ch < '0' || ch > '9' {
							return 0
						}
						kb = kb*10 + uint64(ch-'0')
					}
					return kb / 1024
				}
			}
		}
	}
	return 0
}
