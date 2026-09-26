// Package signaling defines the transport-agnostic control channel contract.
// Phase 1 runs over outbound WebSocket from the node; future transports
// (direct, ICE/TURN) implement the same interface.
package signaling

import "github.com/personal-game/personal-game/pkg/protocol"

// Envelope wraps a command or event on the node channel.
type Envelope struct {
	Type      string               `json:"type"` // "command" | "event" | "heartbeat"
	Command   protocol.NodeCommand `json:"command,omitempty"`
	SessionID string               `json:"session_id,omitempty"`
	Payload   map[string]any       `json:"payload,omitempty"`
}

// Channel is the outbound control link abstraction.
type Channel interface {
	Send(cmd protocol.NodeCommand, sessionID string, payload map[string]any) error
}
