package hub

import "time"

// InboundMessage is a raw message received from a WebSocket client.
type InboundMessage struct {
	ClientID string
	RoomID   string
	UserID   string
	Content  string
	At       time.Time
}

// OutboundMessage is a filtered, broadcast-ready message.
// Type values:
//
//	"message" — normal chat message
//	"warn"    — message was sent but flagged as suspicious (quarantine confirmed)
//	"delete"  — message was sent optimistically but must be retracted (block)
type OutboundMessage struct {
	Type    string    `json:"type"`
	MsgID   string    `json:"msg_id"`
	UserID  string    `json:"user_id,omitempty"`
	Content string    `json:"content,omitempty"`
	At      time.Time `json:"at,omitempty"`
}
