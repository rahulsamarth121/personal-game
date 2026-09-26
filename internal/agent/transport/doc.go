// Package transport abstracts the node-to-control link (phase 1: outbound
// link over Tailscale). Future ICE/TURN transports share this interface.
package transport

// Dialer establishes outbound control connectivity.
type Dialer interface {
	Dial(url string) error
}
