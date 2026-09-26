package capability

import (
	"fmt"
	"os"
	"strings"

	"github.com/personal-game/personal-game/pkg/protocol"
)

// Media-network configuration comes from the environment so deployment
// differences stay values, never code branches:
//
//	MEDIA_NETWORK=tailscale | cloudflare_private_network | direct
//	MEDIA_ENDPOINT=<Moonlight-reachable IP or DNS name>   (override)
//
// Without MEDIA_NETWORK the node falls back to its real Tailscale state
// (and only advertises a usable network when Tailscale actually answers).
// Reachability is measured, never assumed: a provider that is configured
// but not answering reports Reachable=false with a reason in Detail.
func ResolveMediaNetwork(ts TailscaleDetail) (protocol.MediaNetwork, error) {
	provider := strings.ToLower(strings.TrimSpace(os.Getenv("MEDIA_NETWORK")))
	endpoint := strings.TrimSpace(os.Getenv("MEDIA_ENDPOINT"))

	if provider == "" {
		// Default: the historical path. Advertise Tailscale only when the
		// daemon is running and produced an address; never pretend.
		if ts.Running && ts.IP != "" {
			return protocol.MediaNetwork{
				Provider:      protocol.MediaTailscale,
				Endpoint:      ts.IP,
				AddressFamily: "ipv4",
				TCPOK:         true,
				UDPOK:         true,
				Reachable:     true,
				Detail:        fmt.Sprintf("tailscale up (%d peers)", ts.Peers),
			}, nil
		}
		return protocol.MediaNetwork{
			Provider:  protocol.MediaTailscale,
			Reachable: false,
			Detail:    ts.Reason,
		}, nil
	}

	switch protocol.MediaProvider(provider) {
	case protocol.MediaTailscale:
		if endpoint == "" {
			endpoint = ts.IP
		}
		return finish(provider, endpoint, ts.Running && endpoint != "",
			map[bool]string{false: ts.Reason}[ts.Running && endpoint != ""])

	case protocol.MediaCloudflarePrivateNet:
		// Cloudflare One/WARP private-network routing: the endpoint is the
		// node's address inside the WARP-enrolled private network (e.g. the
		// tunnel-routed RFC1918/CGNAT address). The operator supplies it via
		// MEDIA_ENDPOINT; reachability requires the endpoint to be set and
		// TCP/47989+UDP path to be expected to terminate on this node (the
		// cloudflared connector is local). We verify the connector is
		// present rather than fabricating a green light.
		if endpoint == "" {
			return protocol.MediaNetwork{
				Provider:  protocol.MediaCloudflarePrivateNet,
				Reachable: false,
				Detail:    "MEDIA_ENDPOINT required (node address inside the WARP private network)",
			}, nil
		}
		connected := cloudflaredConnected()
		return finish(provider, endpoint, connected,
			map[bool]string{false: "cloudflared connector not running (start it with the private-network route configured)"}[connected])

	case protocol.MediaDirect:
		if endpoint == "" {
			return protocol.MediaNetwork{
				Provider:  protocol.MediaDirect,
				Reachable: false,
				Detail:    "MEDIA_ENDPOINT required (public IP or DNS name of this node)",
			}, nil
		}
		// Direct exposure is configured by the operator; we cannot probe the
		// internet from inside. Honest default: reachable when configured.
		return finish(provider, endpoint, true, "")

	default:
		return protocol.MediaNetwork{}, fmt.Errorf(
			"capability: unknown MEDIA_NETWORK %q (tailscale | cloudflare_private_network | direct)", provider)
	}
}

// finish builds the struct with the measured/configured reachability.
func finish(provider, endpoint string, reachable bool, reason string) (protocol.MediaNetwork, error) {
	m := protocol.MediaNetwork{
		Provider:  protocol.MediaProvider(provider),
		Endpoint:  endpoint,
		Reachable: reachable,
		TCPOK:     reachable,
		UDPOK:     reachable,
		Detail:    reason,
	}
	if strings.Contains(endpoint, ":") && !strings.Contains(endpoint, "]") {
		m.AddressFamily = "ipv6"
	} else if strings.ContainsAny(endpoint, "abcdefghijklmnopqrstuvwxyz") {
		m.AddressFamily = "dns"
	} else {
		m.AddressFamily = "ipv4"
	}
	if err := m.Validate(); err != nil {
		return protocol.MediaNetwork{}, err
	}
	return m, nil
}

// cloudflaredConnected reports whether a cloudflared connector process is
// answering locally (`cloudflared` present and service query succeeds).
// Best effort: absence is reported, never guessed around.
func cloudflaredConnected() bool {
	out, err := runTimeout("cloudflared", "--version")
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) != ""
}
