package capability

import (
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/personal-game/personal-game/pkg/protocol"
)

// gpuAndEncoders performs best-effort GPU detection using tools that may
// exist on the node (nvidia-smi, rocm-smi). Never shells out with user
// input; argv is fixed. Unknown => empty model, no encoders.
func gpuAndEncoders() (protocol.GPUCap, []string) {
	var gpu protocol.GPUCap
	var enc []string
	if _, err := exec.LookPath("nvidia-smi"); err == nil {
		out, err := exec.Command("nvidia-smi",
			"--query-gpu=name,memory.total,driver_version",
			"--format=csv,noheader,nounits").Output()
		if err == nil {
			if line := strings.TrimSpace(string(out)); line != "" {
				parts := strings.Split(line, ",")
				if len(parts) >= 3 {
					gpu.Model = strings.TrimSpace(parts[0])
					gpu.VRAMMB = parseUint(strings.TrimSpace(parts[1]))
					gpu.Driver = strings.TrimSpace(parts[2])
					enc = append(enc, "nvenc-h264", "nvenc-hevc")
				}
			}
		}
	}
	if os.Getenv("PG_FORCE_NO_GPU") == "1" {
		return protocol.GPUCap{}, nil
	}
	_ = runtime.GOOS
	return gpu, enc
}

func parseUint(s string) uint64 {
	var n uint64
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			break
		}
		n = n*10 + uint64(ch-'0')
	}
	return n
}

func runtime_isWindows() bool { return runtime.GOOS == "windows" }

// detectGamepad: /dev/uinput or /dev/input presence on Linux; false elsewhere
// unless PG_FORCE_GAMEPAD=1 (tests).
func detectGamepad() bool {
	if os.Getenv("PG_FORCE_GAMEPAD") == "1" {
		return true
	}
	for _, p := range []string{"/dev/uinput", "/dev/input"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// detectVirtualDisplay: Xvfb / XDG session presence; false when unknown.
func detectVirtualDisplay() bool {
	if os.Getenv("PG_FORCE_VDISPLAY") == "1" {
		return true
	}
	if _, err := exec.LookPath("Xvfb"); err == nil {
		return true
	}
	return false
}
