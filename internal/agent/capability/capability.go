// Package capability discovers the node's structured capabilities at
// startup. Output is provider-agnostic: Kaggle vs LOCAL differences appear
// as different capability VALUES, never as branches in business logic.
package capability

import (
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/personal-game/personal-game/pkg/protocol"
)

// Discover collects best-effort capabilities. It never fails hard: unknown
// fields stay zero-valued so scheduling treats the node as constrained.
// StreamingAllowed is policy (STREAMING_ALLOWED=true), not detection: the
// operator declares this deployment may host interactive game streaming.
// A node without it never receives gaming sessions, whatever its hardware.
func Discover() protocol.Capabilities {
	caps := protocol.Capabilities{
		OS:               runtime.GOOS,
		Arch:             runtime.GOARCH,
		CPUCores:         runtime.NumCPU(),
		StreamingAllowed: envBool("STREAMING_ALLOWED"),
	}
	caps.CPUModel = cpuModel()
	caps.RAMMB = totalRAMMB()
	caps.Disk = diskCap()
	caps.GPU, caps.Encoders = gpuAndEncoders()
	caps.Docker = lookPath("docker")
	caps.Podman = lookPath("podman")
	caps.Wolf = lookPath("wolf") || lookPath("wolf-server")
	caps.Sunshine = lookPath("sunshine")
	caps.Tailscale = lookPath("tailscale")
	if out, err := exec.Command("tailscale", "ip", "--4").Output(); err == nil {
		if ip := strings.TrimSpace(string(out)); ip != "" {
			caps.Network.TailscaleIP = strings.Fields(ip)[0]
		}
	}
	if media, err := ResolveMediaNetwork(Tailscale()); err == nil {
		caps.Network.MediaNetwork = media
	}
	caps.Gamepad = detectGamepad()
	caps.VirtualDisp = detectVirtualDisplay()
	if rt := DetectNVIDIARuntime(); rt.Accelerated {
		caps.ContainerRuntime = "nvidia"
	}
	caps.Streaming = streamingProviders(caps)
	return caps
}

// envBool reads a truthy environment flag ("1", "true", "yes", "on").
func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func lookPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func cpuModel() string {
	// Best-effort without new deps: platform-specific hooks can enrich this.
	// Keep honest: return "" when unknown rather than guessing.
	switch runtime.GOOS {
	case "linux":
		if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(line, "model name") {
					if i := strings.Index(line, ":"); i >= 0 {
						return strings.TrimSpace(line[i+1:])
					}
				}
			}
		}
	}
	return ""
}

func streamingProviders(c protocol.Capabilities) []string {
	var out []string
	if c.Sunshine {
		out = append(out, "sunshine")
	}
	if c.Wolf {
		out = append(out, "wolf")
	}
	return out
}
