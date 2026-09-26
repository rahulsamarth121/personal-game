package unit

// capableCaps describes a node that may host interactive streaming: policy
// bit set, a real backend, and a usable media endpoint. Tests that exercise
// scheduling/session flows use this; tests that specifically verify the
// gating construct disabled nodes inline for clarity.
import "github.com/personal-game/personal-game/pkg/protocol"

func capableCaps() protocol.Capabilities {
	return protocol.Capabilities{
		OS:               "linux",
		Arch:             "amd64",
		GPU:              protocol.GPUCap{Model: "NVIDIA Test GPU", VRAMMB: 8192},
		Wolf:             true,
		StreamingAllowed: true,
		Streaming:        []string{"wolf"},
		Network: protocol.NetworkCap{
			MediaNetwork: protocol.MediaNetwork{
				Provider:      protocol.MediaTailscale,
				Endpoint:      "100.100.1.1",
				AddressFamily: "ipv4",
				TCPOK:         true,
				UDPOK:         true,
				Reachable:     true,
			},
		},
	}
}
