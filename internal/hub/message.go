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
type OutboundMessage struct {
	UserID  string    `json:"user_id"`
	Content string    `json:"content"`
	At      time.Time `json:"at"`
}
