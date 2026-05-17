package hub

import "time"

func timePtr(t time.Time) *time.Time { return &t }

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
//	"message"    — normal chat message
//	"warn"       — message flagged as suspicious; shown immediately on quarantine
//	"clear_warn" — ollama cleared the warning (allow verdict); remove warning UI
//	"delete"     — retract message from chat (ollama block verdict)
//	"block"      — message blocked before broadcast; sent only to the originating client
type OutboundMessage struct {
	Type    string    `json:"type"`
	MsgID   string    `json:"msg_id,omitempty"`
	UserID  string    `json:"user_id,omitempty"`
	Content string     `json:"content,omitempty"`
	At      *time.Time `json:"at,omitempty"`
	Reason  string    `json:"reason,omitempty"`
	Score   float64   `json:"score,omitempty"`
}
