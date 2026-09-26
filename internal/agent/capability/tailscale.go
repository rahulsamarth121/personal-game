package capability

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"
)

// TailscaleDetail reports install/run/address state for the phase-1
// network path. Absence is a normal diagnostic outcome with setup
// guidance, never a silent fallback to an inferior path.
type TailscaleDetail struct {
	Installed bool
	Running   bool
	IP        string
	Peers     int // other tailnet nodes visible (-1 = unknown)
	Reason    string
}

// Tailscale inspects the local Tailscale client: binary present, daemon
// answering (`tailscale status` exit code), and the node's Tailscale IPv4.
func Tailscale() TailscaleDetail {
	var d TailscaleDetail
	if _, err := exec.LookPath("tailscale"); err != nil {
		d.Reason = "tailscale not installed (see https://tailscale.com/download)"
		return d
	}
	d.Installed = true
	if out, err := runTimeout("tailscale", "status"); err != nil {
		d.Reason = "tailscale installed but daemon not running (run: tailscale up): " + firstLine(out)
		return d
	}
	d.Running = true
	if out, err := runTimeout("tailscale", "ip", "--4"); err == nil {
		if ip := strings.TrimSpace(out); ip != "" {
			d.IP = strings.Fields(ip)[0]
		}
	}
	if d.IP == "" {
		d.Reason = "tailscale running but no IPv4 address reported"
		return d
	}
	d.Peers = peerCount()
	return d
}

// peerCount parses `tailscale status --json` for visible peers.
// Best effort: -1 when the daemon will not say.
func peerCount() int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tailscale", "status", "--json").Output()
	if err != nil {
		return -1
	}
	var st struct {
		Peer map[string]any `json:"Peer"`
	}
	if err := json.Unmarshal(out, &st); err != nil || st.Peer == nil {
		return -1
	}
	return len(st.Peer)
}

func runTimeout(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return "", timeoutError(name)
	}
	return string(out), err
}

type timeoutError string

func (e timeoutError) Error() string { return "tailscale: timed out running " + string(e) }

func errTimeout(name string) error { return timeoutError(name) }

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
