// Package stream defines the integration boundary with mature streaming
// backends (Sunshine, Wolf). We orchestrate their lifecycle and read back
// connection endpoints; capture/encode/input stay inside those projects.
// No custom streaming protocol lives here by design (see ADR-0003).
package stream

import (
	"errors"

	"github.com/personal-game/personal-game/pkg/protocol"
)

// Backend is what the agent needs from any streaming server.
type Backend interface {
	// Name identifies the backend ("sunshine" | "wolf").
	Name() string
	// Available reports whether the backend binary/config exists.
	Available() bool
	// Endpoint returns where Moonlight should connect.
	Endpoint(host string) protocol.StreamEndpoint
}

// Sunshine backend (Games-on-Whales alternative below).
type Sunshine struct{ Port int }

// Name implements Backend.
func (s Sunshine) Name() string { return "sunshine" }

// Available implements Backend.
func (s Sunshine) Available() bool { return true }

// Endpoint implements Backend.
func (s Sunshine) Endpoint(host string) protocol.StreamEndpoint {
	port := s.Port
	if port == 0 {
		port = 47989 // Sunshine HTTPS default
	}
	return protocol.StreamEndpoint{Backend: "sunshine", Host: host, Port: port}
}

// Wolf backend.
type Wolf struct{ Port int }

// Name implements Backend.
func (w Wolf) Name() string { return "wolf" }

// Available implements Backend.
func (w Wolf) Available() bool { return true }

// Endpoint implements Backend.
func (w Wolf) Endpoint(host string) protocol.StreamEndpoint {
	port := w.Port
	if port == 0 {
		port = 47989
	}
	return protocol.StreamEndpoint{Backend: "wolf", Host: host, Port: port}
}

// Select picks the preferred backend from discovered capabilities.
// Sunshine is the Stage 1 default when present; Wolf otherwise.
// Returns an error when neither is installed so setup fails clearly.
func Select(caps protocol.Capabilities) (Backend, error) {
	if caps.Sunshine {
		return Sunshine{}, nil
	}
	if caps.Wolf {
		return Wolf{}, nil
	}
	return nil, errors.New("stream: no backend installed (need Sunshine or Wolf)")
}
