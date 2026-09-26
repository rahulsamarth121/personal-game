package capability

import (
	"os/exec"
	"strings"
)

// NVIDIARuntime reports whether containers on this node can see the GPU.
// Both halves must hold: nvidia-smi sees hardware AND the container stack
// (nvidia-ctk/cli tool or a docker nvidia runtime) can expose it.
// nvidia-smi alone never implies container acceleration.
type NVIDIARuntime struct {
	GPUSeen        bool
	ToolkitPresent bool
	DockerNvidiaRT bool
	Accelerated    bool // GPUSeen && (ToolkitPresent || DockerNvidiaRT)
	Detail         string
}

// DetectNVIDIARuntime probes the full chain without side effects.
func DetectNVIDIARuntime() NVIDIARuntime {
	var r NVIDIARuntime
	if _, err := exec.LookPath("nvidia-smi"); err != nil {
		r.Detail = "no nvidia-smi: no NVIDIA GPU/driver visible"
		return r
	}
	if out, err := exec.Command("nvidia-smi", "--query-gpu=name",
		"--format=csv,noheader").Output(); err != nil || len(strings.TrimSpace(string(out))) == 0 {
		r.Detail = "nvidia-smi present but reports no GPUs"
		return r
	}
	r.GPUSeen = true
	for _, bin := range []string{"nvidia-ctk", "nvidia-container-cli"} {
		if _, err := exec.LookPath(bin); err == nil {
			r.ToolkitPresent = true
			break
		}
	}
	if out, err := exec.Command("docker", "info", "--format", "{{json .Runtimes}}").Output(); err == nil {
		r.DockerNvidiaRT = strings.Contains(strings.ToLower(string(out)), "nvidia")
	}
	r.Accelerated = r.GPUSeen && (r.ToolkitPresent || r.DockerNvidiaRT)
	switch {
	case r.Accelerated:
		r.Detail = "NVIDIA GPU visible to containers"
	case r.ToolkitPresent:
		r.Detail = "GPU seen, toolkit present, docker runtime unconfirmed"
	default:
		r.Detail = "GPU seen by nvidia-smi but NO container runtime exposes it (install nvidia-container-toolkit + nvidia-ctk runtime configure)"
	}
	return r
}
